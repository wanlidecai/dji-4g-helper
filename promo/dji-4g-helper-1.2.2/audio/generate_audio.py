#!/usr/bin/env python3
"""Regenerate the 48 s product film's narration, original music, and subtitles.

Text and scene times come exclusively from ../storyboard.json. Install the
dependencies listed in ../../requirements.txt in a virtual environment first.
Usage: python3 generate_audio.py [--voice zh-CN-XiaoxiaoNeural] [--rate 5]
       python3 generate_audio.py --voice zh-CN-YunxiNeural --rate 5
No speech is truncated. A scene exceeding its speech budget is resynthesized
at a slightly faster rate. Network failure falls back to one uniform local
Tingting voice for all eight scenes and is recorded in metadata.json.
"""
from __future__ import annotations
import argparse
import asyncio
import hashlib
import json
import math
from pathlib import Path
import subprocess
import wave

ROOT = Path(__file__).resolve().parent
PROJECT = ROOT.parent
import numpy as np
import imageio_ffmpeg

FFMPEG = imageio_ffmpeg.get_ffmpeg_exe()
SR = 48000
OFFSET = 0.35
SPEECH_LIMIT = 5.3
STORY_PATH = PROJECT / 'storyboard.json'
STORY = json.loads(STORY_PATH.read_text(encoding='utf-8'))
DURATION = float(STORY['duration'])
SCENES = STORY['scenes']

def decode(path: Path) -> np.ndarray:
    raw = subprocess.check_output([FFMPEG, '-v', 'error', '-i', str(path),
        '-ar', str(SR), '-ac', '1', '-f', 'f32le', 'pipe:1'])
    return np.frombuffer(raw, dtype='<f4').copy()

def trim_silence(x: np.ndarray) -> np.ndarray:
    # Only trim leading/trailing silence, keeping a 45 ms margin.
    nz = np.flatnonzero(np.abs(x) > 0.002)
    if len(nz) == 0:
        raise RuntimeError('The speech service returned silence')
    margin = round(0.045 * SR)
    x = x[max(0, int(nz[0]) - margin):min(len(x), int(nz[-1]) + margin + 1)]
    n = min(round(0.004 * SR), len(x) // 2)
    x[:n] *= np.linspace(0, 1, n)
    x[-n:] *= np.linspace(1, 0, n)
    return x

def db(amplitude: float) -> float | None:
    return round(20 * math.log10(amplitude), 3) if amplitude > 0 else None

def stats(x: np.ndarray, name: str) -> dict:
    pcm = np.round(x * 32767).astype('<i2')
    return {'file': name, 'sample_rate': SR,
        'channels': 1 if x.ndim == 1 else x.shape[1], 'samples': len(x),
        'duration_s': round(len(x) / SR, 6),
        'peak_dbfs': db(float(np.max(np.abs(x)))),
        'rms_dbfs': db(float(np.sqrt(np.mean(x * x)))),
        'clipped_samples': int(np.count_nonzero(np.abs(pcm.astype(np.int32)) >= 32767))}

def save_wav(path: Path, x: np.ndarray) -> dict:
    if not np.isfinite(x).all() or float(np.max(np.abs(x))) >= 0.999:
        raise RuntimeError(f'Invalid or clipping audio: {path.name}')
    with wave.open(str(path), 'wb') as f:
        f.setnchannels(1 if x.ndim == 1 else x.shape[1])
        f.setsampwidth(2)
        f.setframerate(SR)
        f.writeframes(np.round(x * 32767).astype('<i2').tobytes())
    return stats(x, path.name)

async def speech_edge(scene: dict, voice_name: str, base_rate: int):
    import edge_tts
    path = ROOT / f"voice_{scene['index']:02d}_source.mp3"
    for rate in range(base_rate, 26, 5):
        for retry in range(2):
            try:
                await asyncio.wait_for(edge_tts.Communicate(scene['voice_text'],
                    voice_name, rate=f'+{rate}%').save(str(path)), timeout=45)
                break
            except Exception:
                if retry == 1:
                    raise
                await asyncio.sleep(1)
        x = trim_silence(decode(path))
        if len(x) / SR <= SPEECH_LIMIT:
            return x, {'engine': 'edge-tts', 'voice': voice_name, 'rate': f'+{rate}%'}
    raise RuntimeError(f"Scene {scene['index']} exceeds {SPEECH_LIMIT} s at +25%")

def speech_macos(scene: dict):
    path = ROOT / f"voice_{scene['index']:02d}_source.aiff"
    for rate in (195, 210, 225, 240, 255):
        subprocess.run(['say', '-v', 'Tingting', '-r', str(rate), '-o', str(path),
            scene['voice_text']], check=True)
        x = trim_silence(decode(path))
        if len(x) / SR <= SPEECH_LIMIT:
            return x, {'engine': 'macOS say', 'voice': 'Tingting', 'rate_wpm': rate}
    raise RuntimeError(f"Scene {scene['index']} exceeds its local speech budget")

def hz(midi: int) -> float:
    return 440 * 2 ** ((midi - 69) / 12)

def make_music() -> np.ndarray:
    """A new original airy major/add9 score, 100 BPM, synthesized from waves.

    Soft glass/plucked notes, rounded bass, a very light brush pulse. No
    recordings, samples, commercial compositions, or borrowed melodies.
    """
    count = round(DURATION * SR)
    music = np.zeros((count, 2), dtype=np.float64)
    rng = np.random.default_rng(20261008121)
    beat = 0.6
    def put(start, x, pan=0.0, gain=1.0):
        first = round(start * SR)
        if first < 0:
            x = x[-first:]
            first = 0
        end = min(count, first + len(x))
        x = x[:end-first]
        if len(x) == 0:
            return
        angle = (pan + 1) * math.pi / 4
        music[first:end, 0] += x * math.cos(angle) * gain
        music[first:end, 1] += x * math.sin(angle) * gain
    chords = [
        [48, 55, 59, 62, 64],  # Cmaj9
        [41, 48, 52, 55, 57],  # Fmaj9
        [43, 50, 55, 59, 64],  # G6
        [45, 52, 55, 59, 60],  # Am9
        [41, 48, 52, 55, 57],
        [43, 50, 55, 57, 59],
        [48, 55, 59, 62, 64],
        [48, 55, 59, 62, 64],
    ]
    for block, chord in enumerate(chords):
        start = block * 6.0
        t = np.arange(round(6.9 * SR)) / SR
        env = np.maximum(0, np.minimum(t / 0.65, 1) * np.minimum((6.9-t) / 1.1, 1))
        for j, midi in enumerate(chord):
            freq = hz(midi+12)
            pad = (np.sin(2*math.pi*freq*t+j*0.4)
                + 0.12*np.sin(2*math.pi*freq*2*t)) * env * 0.020
            put(start, pad, pan=(-0.60,-0.30,0,0.30,0.60)[j])
        motif = [2, 4, 3, 1, 2, 4, 3, 4, 2, 1]
        for k, tone in enumerate(motif):
            t = np.arange(round(0.96*SR)) / SR
            envelope = (1-np.exp(-t*130)) * np.exp(-t*6.2)
            f = hz(chord[tone]+24)
            pluck = (np.sin(2*math.pi*f*t)+0.15*np.sin(2*math.pi*f*2.003*t)) * envelope * 0.042
            pan = -0.40 if k % 2 == 0 else 0.40
            put(start+k*beat, pluck, pan)
            put(start+k*beat+0.24, pluck, -pan, 0.19)
        for k in range(10):
            t = np.arange(round(0.5*SR))/SR
            bass = np.sin(2*math.pi*hz(chord[0])*t)
            bass *= (1-np.exp(-t*90))*np.exp(-t*5.8)*0.045
            put(start+k*beat, bass)
    for k in range(80):
        start = k*beat
        if start < 3.0 or start >= 45.0:
            continue
        t = np.arange(round(0.20*SR))/SR
        phase = 2*math.pi*(48*t+36*0.036*(1-np.exp(-t/0.036)))
        kick = np.sin(phase)*np.exp(-t*22)*0.034
        put(start,kick)
        if k % 2 == 1:
            t = np.arange(round(0.10*SR))/SR
            noise = rng.normal(0,1,len(t))
            brush = np.convolve(noise,np.ones(7)/7,mode='same')*np.exp(-t*48)*0.011
            put(start,brush,0.24)
    for boundary in range(1,8):
        t = np.arange(round(0.34*SR))/SR
        noise = np.convolve(rng.normal(0,1,len(t)),np.ones(17)/17,mode='same')
        swoosh = noise*np.sin(math.pi*t/0.34)**2*0.020
        put(boundary*6-0.19,swoosh,-0.18+boundary/25)
        t = np.arange(round(0.48*SR))/SR
        ping = np.sin(2*math.pi*hz(84 if boundary%2 else 79)*t)
        ping *= (1-np.exp(-t*160))*np.exp(-t*10)*0.008
        put(boundary*6+0.02,ping,0.30)
    t = np.arange(count)/SR
    music *= (np.minimum(t/0.85,1)*np.minimum((DURATION-t)/1.8,1))[:,None]
    music *= 0.17 / np.max(np.abs(music))
    return music.astype(np.float32)

def measure_loudness(path: Path) -> dict:
    r = subprocess.run([FFMPEG,'-hide_banner','-i',str(path),'-af',
        'loudnorm=I=-18:TP=-1.5:LRA=11:print_format=json','-f','null','/dev/null'],
        capture_output=True,text=True,check=True)
    return json.loads(r.stderr[r.stderr.rfind('{'):r.stderr.rfind('}')+1])

def srt_stamp(seconds: float) -> str:
    total = round(seconds*1000)
    h,total = divmod(total,3600000)
    m,total = divmod(total,60000)
    s,ms = divmod(total,1000)
    return f'{h:02d}:{m:02d}:{s:02d},{ms:03d}'

async def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--voice',default=STORY.get('voice','zh-CN-XiaoxiaoNeural'))
    p.add_argument('--rate',type=int,default=5,choices=(5,10))
    args = p.parse_args()
    assert len(SCENES)==8 and DURATION==48
    fallback = None
    results = []
    try:
        for scene in SCENES:
            result = await speech_edge(scene,args.voice,args.rate)
            results.append(result)
            print(f"Scene {scene['index']}: {len(result[0])/SR:.3f} s, {result[1]['voice']}, {result[1]['rate']}",flush=True)
    except Exception as e:
        fallback = type(e).__name__
        print(f'Cloud speech unavailable ({fallback}); regenerating every scene with local Tingting.',flush=True)
        results = [speech_macos(s) for s in SCENES]
    total = round(DURATION*SR)
    voice = np.zeros(total,dtype=np.float32)
    music = make_music()
    duck = np.ones(total,dtype=np.float32)
    metadata = {'duration_s':DURATION,'scene_length_s':6,'narration_offset_s':OFFSET,
        'speech_limit_s':SPEECH_LIMIT,'requested_voice':args.voice,'fallback_reason':fallback,
        'storyboard_sha256':hashlib.sha256(STORY_PATH.read_bytes()).hexdigest(),
        'music':{'composition':'Original synthesis: warm major/add9 soft glass motif',
            'bpm':100,'seed':20261008121,'sample_sources':'None','voice_duck_target_db':-15},
        'ffmpeg':FFMPEG,'segments':[]}
    for scene,(x,details) in zip(SCENES,results):
        x *= 0.70/max(float(np.max(np.abs(x))),0.0001)
        seg_stats = save_wav(ROOT/f"voice_{scene['index']:02d}.wav",x)
        start_s = float(scene['start'])+OFFSET
        first=round(start_s*SR)
        last=first+len(x)
        if len(x)/SR>SPEECH_LIMIT or last>round(float(scene['end'])*SR):
            raise RuntimeError('Speech crosses its scene; refusing to truncate')
        voice[first:last]+=x
        vrms=float(np.sqrt(np.mean(x*x)))
        mrms=float(np.sqrt(np.mean(music[first:last]**2)))
        depth=min(1.0,vrms*10**(-15/20)/max(mrms,1e-6))
        attack=round(0.15*SR)
        release=round(0.30*SR)
        attack_start=max(0,first-attack)
        duck[attack_start:first]=np.minimum(duck[attack_start:first],np.linspace(1,depth,first-attack_start))
        duck[first:last]=np.minimum(duck[first:last],depth)
        stop=min(total,last+release)
        duck[last:stop]=np.minimum(duck[last:stop],np.linspace(depth,1,stop-last))
        metadata['segments'].append({'scene':scene['index'],'text':scene['voice_text'],
            'caption':scene['caption'],'start_s':start_s,'end_s':round(last/SR,6),
            'music_gain_during_speech':round(depth,5),**details,**seg_stats})
    stereo_voice=np.column_stack([voice,voice])
    ducked_music=music*duck[:,None]
    mix=stereo_voice+ducked_music
    if np.max(np.abs(mix))>0.82:
        gain=0.82/float(np.max(np.abs(mix)))
        stereo_voice*=gain
        ducked_music*=gain
        mix*=gain
    else:
        gain=1.0
    metadata['pre_master_gain']=gain
    metadata['outputs']={
        'voice':save_wav(ROOT/'voice.wav',stereo_voice),
        'music':save_wav(ROOT/'music.wav',music),
        'music_ducked':save_wav(ROOT/'music_ducked.wav',ducked_music),
        'mix':save_wav(ROOT/'mix.wav',mix)}
    measured=measure_loudness(ROOT/'mix.wav')
    normalizer=('loudnorm=I=-18:TP=-1.5:LRA=11:'
        f"measured_I={measured['input_i']}:measured_TP={measured['input_tp']}:"
        f"measured_LRA={measured['input_lra']}:measured_thresh={measured['input_thresh']}:"
        f"offset={measured['target_offset']}:linear=true")
    subprocess.run([FFMPEG,'-v','error','-y','-i',str(ROOT/'mix.wav'),'-af',normalizer,
        '-ar',str(SR),'-c:a','pcm_s16le',str(ROOT/'mix_mastered.wav')],check=True)
    with wave.open(str(ROOT/'mix_mastered.wav'),'rb') as f:
        mastered=np.frombuffer(f.readframes(f.getnframes()),dtype='<i2').astype(np.float32).reshape(-1,2)/32768
    actual=measure_loudness(ROOT/'mix_mastered.wav')
    metadata['outputs']['mastered_mix']=stats(mastered,'mix_mastered.wav')
    metadata['mastering']={'target_lufs':-18,'true_peak_limit_db':-1.5,
        'raw_lufs':float(measured['input_i']),'measured_lufs':float(actual['input_i']),
        'measured_true_peak_db':float(actual['input_tp']),
        'measured_loudness_range_lu':float(actual['input_lra'])}
    # This is measured after all duck envelopes have been combined.
    for seg in metadata['segments']:
        a=round(seg['start_s']*SR);b=round(seg['end_s']*SR)
        vocal=float(np.sqrt(np.mean(stereo_voice[a:b]**2)))
        bed=float(np.sqrt(np.mean(ducked_music[a:b]**2)))
        seg['music_vs_voice_rms_db']=round(20*math.log10(bed/vocal),3)
        assert seg['music_vs_voice_rms_db']<=-14.95
    for output in metadata['outputs'].values():
        assert output['samples']==total and output['clipped_samples']==0
    assert float(actual['input_tp'])<=-1.45
    assert abs(float(actual['input_i'])+18)<0.2
    (ROOT/'metadata.json').write_text(json.dumps(metadata,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    lines=[]
    for scene,seg in zip(SCENES,metadata['segments']):
        lines.append(f"{scene['index']}\n{srt_stamp(seg['start_s'])} --> {srt_stamp(seg['end_s'])}\n{scene['caption']}\n")
    (PROJECT/'中文字幕.srt').write_text('\n'.join(lines),encoding='utf-8')
    (PROJECT/'配音稿.txt').write_text('\n'.join(f"{s['index']}. {s['voice_text']}" for s in SCENES)+'\n',encoding='utf-8')
    print(json.dumps({'engine':metadata['segments'][0]['engine'],
        'voice':metadata['segments'][0]['voice'],'mastering':metadata['mastering'],
        'output':str(ROOT/'mix_mastered.wav')},ensure_ascii=False),flush=True)

if __name__=='__main__':
    asyncio.run(main())
