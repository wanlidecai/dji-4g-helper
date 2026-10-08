#!/usr/bin/env python3
"""Warm product motion graphics; all illustrated interface data is synthetic.

Run --stills for QA, --format landscape|portrait to render, then --mux.
No application, network, modem, or messaging actions are performed.
"""
from pathlib import Path
from functools import lru_cache
import argparse, json, math, subprocess, time
import numpy as np
from PIL import Image, ImageDraw, ImageFont, ImageFilter

ROOT = Path(__file__).resolve().parent
PROJECT = ROOT.parent.parent
STORY = json.loads((ROOT / 'storyboard.json').read_text())
DURATION, FPS = STORY['duration'], STORY['fps']
CREAM = (248, 247, 242)
WHITE = (255, 254, 250)
INK = (39, 53, 43)
GREEN = (40, 99, 78)
SAGE = (229, 237, 223)
MUTED = (116, 127, 113)
BORDER = (223, 227, 216)
AMBER = (159, 127, 67)
FONT = '/System/Library/Fonts/Hiragino Sans GB.ttc'
LATIN = '/System/Library/Fonts/Avenir Next.ttc'
LATIN_REGULAR = '/System/Library/Fonts/Supplemental/Arial.ttf'
LATIN_BOLD = '/System/Library/Fonts/Supplemental/Arial Bold.ttf'

def clamp(x): return max(0., min(1., x))
def ease(x): return 1 - (1-clamp(x))**3
def smooth(x):
    x=clamp(x); return x*x*(3-2*x)

@lru_cache(maxsize=120)
def font(size, bold=False, latin=False):
    if latin and Path(LATIN_REGULAR).is_file():
        return ImageFont.truetype(LATIN_BOLD if bold else LATIN_REGULAR, int(size))
    return ImageFont.truetype(LATIN if latin else FONT, int(size), index=1 if bold else 0)

@lru_cache(maxsize=3000)
def tile(s,size,color,bold=False,latin=False):
    f=font(size,bold,latin); box=f.getbbox(s)
    im=Image.new('RGBA',(max(1,box[2]-box[0]+8),max(1,box[3]-box[1]+8)))
    ImageDraw.Draw(im).text((4-box[0],4-box[1]),s,font=f,fill=color)
    return im

def paste(im,obj,x,y,alpha=1):
    if alpha<=0: return
    if alpha<.999:
        obj=obj.copy(); obj.putalpha(obj.getchannel('A').point(lambda a:int(a*alpha)))
    im.paste(obj,(round(x),round(y)),obj)

def text(im,s,x,y,size=28,color=INK,bold=False,center=False,alpha=1,latin=False):
    obj=tile(s,size,tuple(color),bold,latin)
    paste(im,obj,x-obj.width/2 if center else x,y,alpha)

def rr(im,box,r=24,fill=WHITE,outline=None,width=1):
    ImageDraw.Draw(im).rounded_rectangle(tuple(round(v) for v in box),radius=r,fill=fill,outline=outline,width=width)

def line(im,pts,color=GREEN,width=3):
    ImageDraw.Draw(im).line([(round(x),round(y)) for x,y in pts],fill=color,width=width,joint='curve')

@lru_cache(maxsize=64)
def shadow(w,h,r):
    im=Image.new('RGBA',(w+100,h+100))
    ImageDraw.Draw(im).rounded_rectangle((50,50,w+50,h+50),radius=r,fill=(37,69,48,26))
    return im.filter(ImageFilter.GaussianBlur(20))

def card(im,x,y,w,h,fill=WHITE,r=28,shadowed=True):
    if shadowed:paste(im,shadow(w,h,r),x-50,y-34)
    rr(im,(x,y,x+w,y+h),r,fill,BORDER,2)

@lru_cache(maxsize=1)
def logo():
    source = ROOT/'assets/AppIcon.png'
    if not source.is_file(): source = PROJECT/'mac-app/build/AppIcon-preview.png'
    return Image.open(source).convert('RGBA')

@lru_cache(maxsize=60)
def logo_at(size):return logo().resize((int(size),int(size)),Image.Resampling.LANCZOS)

def check(im,x,y,s=1,color=GREEN):
    line(im,[(x,y+10*s),(x+9*s,y+19*s),(x+29*s,y)],color,max(3,round(5*s)))

def chip(im,s,x,y,size=25,fill=SAGE,color=GREEN):
    obj=tile(s,size,tuple(color),True)
    rr(im,(x,y,x+obj.width+40,y+obj.height+30),20,fill)
    paste(im,obj,x+20,y+15)
    return obj.width+40

def dot(im,x,y,r=7,color=GREEN):
    ImageDraw.Draw(im).ellipse((x-r,y-r,x+r,y+r),fill=color)

def bars(im,x,y,t,size=16):
    for i in range(4):
        h=size*(i+1)
        col=GREEN if i<3 or math.sin(t*2)>.2 else (183,202,174)
        rr(im,(x+i*(size+6),y-h,x+i*(size+6)+size,y),4,col)

def bell(im,x,y,size=44,color=GREEN):
    d=ImageDraw.Draw(im)
    d.arc((x-size*.35,y-size*.3,x+size*.35,y+size*.55),180,360,fill=color,width=max(3,round(size*.07)))
    line(im,[(x-size*.35,y+size*.12),(x-size*.43,y+size*.55),(x+size*.43,y+size*.55),(x+size*.35,y+size*.12)],color,max(3,round(size*.07)))
    d.arc((x-size*.13,y+size*.53,x+size*.13,y+size*.81),0,180,fill=color,width=3)

def envelope(im,x,y,w=46,color=GREEN):
    rr(im,(x,y,x+w,y+w*.7),7,None,color,3)
    line(im,[(x+2,y+3),(x+w*.5,y+w*.4),(x+w-2,y+3)],color,3)

def phone(im,x,y,size=46,color=GREEN):
    d=ImageDraw.Draw(im)
    d.arc((x,y,x+size,y+size),35,145,fill=color,width=max(5,round(size*.16)))
    line(im,[(x+size*.07,y+size*.6),(x+size*.15,y+size*.82),(x+size*.29,y+size*.78)],color,7)
    line(im,[(x+size*.7,y+size*.78),(x+size*.85,y+size*.82),(x+size*.93,y+size*.6)],color,7)

def cursor(im,x,y,click=False):
    if click:ImageDraw.Draw(im).ellipse((x-25,y-25,x+25,y+25),outline=GREEN,width=3)
    ImageDraw.Draw(im).polygon([(x,y),(x+7,y+35),(x+15,y+23),(x+27,y+25)],fill=GREEN,outline=WHITE,width=2)

def note(im,s,y=676,color=MUTED):text(im,s,500,y,23,color,center=True)

def window(im,x=28,y=32,w=944,h=620):
    card(im,x,y,w,h)
    for i,c in enumerate([(226,138,126),(223,194,126),(138,179,144)]):dot(im,x+27+i*23,y+25,6,c)
    text(im,'大疆4g模块辅助工具',x+w/2,y+13,20,MUTED,center=True)
    line(im,[(x+1,y+50),(x+w-1,y+50)],BORDER,1)
    return x,y+50,w,h-50

def sidebar(im,x,y,h,selected='首页'):
    rr(im,(x,y,x+170,y+h),20,CREAM)
    paste(im,logo_at(44),x+20,y+23);text(im,'大疆4g',x+72,y+25,17,INK,True);text(im,'模块辅助工具',x+72,y+50,14,INK,True)
    for i,s in enumerate(['首页','短信','通话','设置']):
        yy=y+114+i*66
        if s==selected:rr(im,(x+14,yy-9,x+156,yy+43),14,SAGE)
        text(im,s,x+50,yy+1,25,GREEN if s==selected else MUTED,True)
    dot(im,x+25,y+h-58,5);text(im,'模块已连接',x+42,y+h-72,21,GREEN)

def overview(st,t):
    im=Image.new('RGBA',(1000,720));x,y,w,h=window(im)
    sidebar(im,x+9,y+8,h-17)
    bx=x+206
    text(im,'网络概览',bx,y+32,37,INK,True)
    chip(im,'LTE · 已连接',bx+480,y+31,21)
    for i,(name,value) in enumerate([('运营商','示例运营商'),('信号','-72 dBm'),('网络','LTE')]):
        xx=bx+i*235
        card(im,xx,y+115,216,128,CREAM,20,False)
        text(im,name,xx+18,y+137,23,MUTED)
        text(im,value,xx+18,y+183,27,GREEN,True)
    card(im,bx,y+275,686,202,WHITE,22,False)
    text(im,'本次流量',bx+25,y+296,25,MUTED)
    text(im,'0.82 MB',bx+25,y+339,53,INK,True,latin=True)
    text(im,'下载速度',bx+390,y+304,22,MUTED);text(im,'128 KB/s',bx+390,y+339,34,GREEN,True,latin=True)
    text(im,'上传速度',bx+390,y+391,22,MUTED);text(im,'24 KB/s',bx+390,y+423,28,GREEN,True,latin=True)
    pts=[]
    for k in range(24):
        xx=bx+26+k*11.4;yy=y+445-20*(.6+.4*math.sin(k*.66+t*1.4))
        pts.append((xx,yy))
    line(im,pts,(136,171,131),3)
    chip(im,'4G 上网模式',bx,y+503,22)
    bars(im,bx+563,y+546,t,11)
    note(im,'界面示意 · 示例数据')
    return im

def opening(st,t):
    im=Image.new('RGBA',(1000,720));d=ImageDraw.Draw(im)
    cx,cy=510,340
    for i in range(3):
        q=(st*.16+i/3)%1;r=135+q*154
        d.ellipse((cx-r,cy-r,cx+r,cy+r),outline=(214-round(q*10),226-round(q*6),208-round(q*8)),width=3)
    s=230+round(10*math.sin(st*.65));paste(im,logo_at(s),cx-s/2,cy-s/2)
    a=ease((st-.45)/.75)
    layer=Image.new('RGBA',(1000,720))
    card(layer,62,140,251,107);bars(layer,91,212,t,9);text(layer,'网络首页',163,174,27,GREEN,True)
    card(layer,718,365,228,110);envelope(layer,744,405,37);text(layer,'短信消息',802,402,26,GREEN,True)
    card(layer,170,544,268,88);dot(layer,199,588,7);text(layer,'安静留在后台',221,573,26,GREEN,True)
    paste(im,layer,0,0,a)
    note(im,'4G 模块管理 · 连接与消息，一处照看')
    return im

def background(st,t):
    im=Image.new('RGBA',(1000,720));card(im,51,76,898,544,CREAM,28)
    rr(im,(73,96,927,146),14,SAGE);text(im,'菜单栏',97,112,23,MUTED)
    paste(im,logo_at(35),636,103);text(im,'大疆4g模块辅助工具',686,109,22,GREEN,True)
    show=1-smooth((st-1.5)/.7)
    if show>.003:
        obj=overview(st,t).crop((0,0,1000,662)).resize((640,424),Image.Resampling.LANCZOS)
        size=max(1,round(640*show));hh=max(1,round(424*show))
        obj=obj.resize((size,hh),Image.Resampling.LANCZOS)
        px=180+(830-180)*(1-show);py=175+(106-175)*(1-show)
        paste(im,obj,px,py,show)
        if st<1.9:cursor(im,201,196,1.2<st<1.7)
    a=ease((st-2)/.6)
    if a:
        layer=Image.new('RGBA',(1000,720))
        paste(layer,logo_at(150),425,255)
        for i in range(3):dot(layer,466+i*34,451,7,(71,118+round(16*math.sin(t*3+i)),82))
        text(layer,'后台服务 · 运行中',500,497,34,GREEN,True,center=True)
        text(layer,'关闭窗口后仍接收消息',500,553,25,MUTED,center=True)
        paste(im,layer,0,0,a)
    note(im,'界面示意 · 示例数据 · 关闭窗口后继续运行')
    return im

def wake(st,t):
    im=Image.new('RGBA',(1000,720));card(im,81,63,838,562)
    text(im,'醒来，先检查连接',500,105,37,INK,True,center=True)
    yy=275
    for i,(label,x) in enumerate([('电脑唤醒',220),('检测网络',500),('保持连接',780)]):
        active=st>(.7+i*.9)
        rr(im,(x-72,yy-72,x+72,yy+72),34,SAGE if active else CREAM,BORDER,2)
        if i==0:
            rr(im,(x-35,yy-27,x+35,yy+20),8,None,GREEN,4)
            line(im,[(x,yy+21),(x,yy+36),(x-23,yy+36),(x+23,yy+36)],GREEN,4)
            if active:dot(im,x+36,yy-33,11,(215,184,114))
        elif i==1:
            bars(im,x-42,yy+34,t,15)
        else:
            check(im,x-26,yy-8,1.7)
        text(im,label,x,yy+105,29,GREEN if active else MUTED,True,center=True)
        if i<2:
            line(im,[(x+91,yy),(x+177,yy)],BORDER,4)
            for j in range(3):
                q=(st*.44+j/3)%1;dot(im,x+91+86*q,yy,4,GREEN if st>1+i*.9 else BORDER)
    a=ease((st-2.6)/.6)
    layer=Image.new('RGBA',(1000,720))
    chip(layer,'网络正常，就不重新切换',294,447,28)
    text(layer,'连续确认断网后，再尝试恢复',500,530,25,MUTED,center=True)
    paste(im,layer,0,0,a)
    note(im,'恢复流程示意 · 结果取决于设备与网络状态')
    return im

def sms(st,t):
    im=Image.new('RGBA',(1000,720));x,y,w,h=window(im);sidebar(im,x+9,y+8,h-17,'短信')
    bx=x+206;text(im,'短信',bx,y+32,38,INK,True);chip(im,'写短信',bx+535,y+30,23)
    card(im,bx,y+110,210,413,CREAM,20,False)
    text(im,'全部短信',bx+19,y+134,25,GREEN,True)
    for i,s in enumerate(['10086','示例联系人','服务提醒']):
        yy=y+208+i*94
        if i==0:rr(im,(bx+10,yy-13,bx+200,yy+62),14,SAGE)
        text(im,s,bx+22,yy,25,INK,True);text(im,'示例消息',bx+22,yy+36,21,MUTED)
    card(im,bx+229,y+110,457,413,WHITE,20,False)
    text(im,'10086 · 示例短信',bx+256,y+143,27,INK,True)
    rr(im,(bx+255,y+210,bx+656,y+292),16,CREAM)
    text(im,'你的验证码是 123456',bx+272,y+237,25,INK)
    text(im,'识别到验证码',bx+258,y+328,23,MUTED)
    text(im,'1 2 3 4 5 6',bx+254,y+371,43,GREEN,True,latin=True)
    chip(im,'复制验证码',bx+451,y+439,23)
    cursor(im,bx+560,y+471,2.3<st<2.7)
    if st>2.7:
        a=ease((st-2.7)/.3);layer=Image.new('RGBA',(1000,720))
        rr(layer,(384,554,832,621),18,GREEN);check(layer,409,575,.75,WHITE)
        text(layer,'验证码已复制 · 动画演示',447,575,23,WHITE,True)
        paste(im,layer,0,0,a)
    note(im,'界面示意 · 示例数据 · 号码与短信均为示例')
    return im

def notifications(st,t):
    im=Image.new('RGBA',(1000,720));d=ImageDraw.Draw(im)
    for i in range(3):
        r=130+i*55;d.ellipse((500-r,347-r,500+r,347+r),outline=BORDER,width=2)
    entries=[('新短信','10086 · 收到一条示例短信'),('来电','示例联系人 · 正在呼入'),('未接来电','你有一条未接来电提醒')]
    for i,(title,body) in enumerate(entries):
        a=ease((st-.25-i*.9)/.6);xx=129+370*(1-a);yy=62+i*173
        if a<=0:continue
        layer=Image.new('RGBA',(1000,720));card(layer,xx,yy,742,141,r=26)
        paste(layer,logo_at(66),xx+25,yy+30)
        text(layer,'大疆4g模块辅助工具',xx+113,yy+20,22,MUTED)
        text(layer,title,xx+113,yy+55,29,INK,True)
        text(layer,body,xx+113,yy+96,24,MUTED)
        bell(layer,xx+683,yy+56,38)
        paste(im,layer,0,0,a)
    chip(im,'新短信 · 来电 · 未接来电',306,605,26)
    note(im,'界面示意 · 示例数据｜需允许系统通知；电脑语音尚未实现',681,AMBER)
    return im

def platforms(st,t):
    im=Image.new('RGBA',(1000,720));a=ease(st/.7)
    for i,(name,ext,sub) in enumerate([('Mac','App','Apple 芯片 · macOS 13+'),('Windows','EXE','Windows x64 打包版本')]):
        x=66+i*454;yy=91+35*(1-ease((st-i*.25)/.7))
        card(im,x,yy,415,488)
        paste(im,logo_at(105),x+155,yy+43)
        text(im,name,x+207,yy+195,48,INK,True,center=True,latin=True)
        text(im,sub,x+207,yy+265,24,MUTED,center=True)
        badge=tile(ext,36,GREEN,True,True)
        rr(im,(x+96,yy+329,x+319,yy+412),20,SAGE)
        paste(im,badge,x+207-badge.width/2,yy+348)
        check(im,x+114,yy+352,1.05)
    note(im,'Windows 已提供安装包，兼容性待实机验证',666,AMBER)
    return im

def closing(st,t):
    im=Image.new('RGBA',(1000,720));cx,cy=500,290
    for i in range(3):
        q=(st*.16+i/3)%1;r=152+q*150
        ImageDraw.Draw(im).ellipse((cx-r,cy-r,cx+r,cy+r),outline=BORDER,width=2)
    size=258+round(6*math.sin(t*.8));paste(im,logo_at(size),cx-size/2,cy-size/2)
    text(im,'连接更清楚，消息更有序',500,483,35,GREEN,True,center=True)
    text(im,'网络首页  /  后台常驻  /  消息提醒',500,550,25,MUTED,center=True)
    label='打开大疆4g模块辅助工具';badge_width=tile(label,28,GREEN,True).width+40;chip(im,label,(1000-badge_width)/2,603,28)
    note(im,'独立第三方工具，非大疆官方应用',682)
    return im

VISUALS=[opening,overview,background,wake,sms,notifications,platforms,closing]
CHAPTERS=['产品亮相','网络首页','后台常驻','唤醒检查','独立短信','消息通知','双平台打包','大疆4g模块辅助工具']

@lru_cache(maxsize=2)
def backdrop(w,h):
    yy,xx=np.mgrid[0:h,0:w]
    glow=np.exp(-(((xx-w*.82)/(w*.58))**2+((yy-h*.43)/(h*.65))**2))
    rgb=np.stack([248-9*glow,247-3*glow,242-13*glow],axis=-1)
    return Image.fromarray(np.uint8(rgb)).convert('RGBA')

def wrap(s,size,max_width):
    result=[];row=''
    for c in s:
        if tile(row+c,size,INK).width>max_width and row:result.append(row);row=c
        else:row+=c
    if row:result.append(row)
    return result

def content(scene,st,t,mode):
    portrait=mode=='portrait';w,h=(1080,1920) if portrait else (1920,1080)
    im=backdrop(w,h).copy();d=ImageDraw.Draw(im)
    for i in range(12):
        xx=(.6+.35*math.sin(i*1.73)) * w+9*math.sin(t*.37+i)
        yy=(.5+.37*math.cos(i*1.07))*h
        dot(im,xx,yy,2,(187,206,178))
    story=STORY['scenes'][scene];entrance=ease(st/.65)
    tx=72 if portrait else 112;ty=225 if portrait else 279
    title_size=76 if portrait else 71
    title_rows=['大疆4g模块','辅助工具'] if scene in (0,7) else (wrap(story['title'],title_size,920) if portrait else story['title'].split('，'))
    for j,s in enumerate(title_rows):
        a=ease((st-j*.12)/.7)
        text(im,s,tx+25*(1-a),ty+j*(title_size+25)+15*(1-a),title_size,INK,True,alpha=a)
    sub_y=ty+len(title_rows)*(title_size+25)+20
    sub_rows=wrap(story['subtitle'],34 if portrait else 31,925 if portrait else 600)
    for j,s in enumerate(sub_rows):text(im,s,tx,sub_y+j*48,34 if portrait else 31,GREEN,True,alpha=ease((st-.3)/.6))
    if not portrait:
        rr(im,(tx,sub_y+87,tx+64,sub_y+92),2,GREEN)
        text(im,f'{scene+1:02d} / {CHAPTERS[scene]}',tx,sub_y+127,25,MUTED,alpha=ease((st-.5)/.6))
    visual=VISUALS[scene](st,t)
    target_w=956 if portrait else 1016
    visual=visual.resize((target_w,round(visual.height*target_w/1000)),Image.Resampling.LANCZOS)
    vx=(w-visual.width)/2 if portrait else 778
    vy=634 if portrait else 198
    paste(im,visual,vx+25*(1-entrance),vy+20*(1-entrance),entrance)
    if portrait:
        text(im,CHAPTERS[scene],w/2,1491,29,MUTED,center=True)
        rr(im,(w/2-27,1540,w/2+27,1544),2,(154,180,143))
    return im

def frame(t,mode='landscape'):
    portrait=mode=='portrait';w,h=(1080,1920) if portrait else (1920,1080)
    scene=min(7,int(t//6));st=t-scene*6
    im=content(scene,st,t,mode)
    if st>5.56 and scene<7:
        nxt=content(scene+1,0,t,mode)
        im=Image.blend(im,nxt,smooth((st-5.56)/.44))
    margin=72 if portrait else 112
    paste(im,logo_at(62),margin,60)
    text(im,'大疆4g模块辅助工具',margin+84,67,33,INK,True)
    text(im,'4G 模块助手',margin+84,108,21,MUTED)
    text(im,f'{scene+1:02d} / 08',w-margin-60,81,25,MUTED,center=True,latin=True)
    if .25<st<5.8:
        alpha=min(ease((st-.25)/.15),ease((5.8-st)/.15))
        size=40 if portrait else 36
        caption=STORY['scenes'][scene]['caption']
        max_width=w-180 if portrait else 1570
        rows=wrap(caption,size,max_width)
        if portrait and len(rows)>1:
            if scene==7:
                rows=['大疆4g模块辅助工具，','把连接和消息，照顾得井井有条。']
            else:
                # Prefer complete phrases over an orphaned last word.
                candidates=[]
                for cut,char in enumerate(caption[:-1],1):
                    if char not in '，。；':continue
                    left,right=caption[:cut],caption[cut:]
                    lw,rw=tile(left,size,WHITE).width,tile(right,size,WHITE).width
                    if max(lw,rw)<=max_width:candidates.append((abs(lw-rw),[left,right]))
                if candidates:rows=min(candidates,key=lambda row:row[0])[1]
        tiles=[tile(s,size,WHITE) for s in rows]
        box_w=max(x.width for x in tiles)+64;box_h=32+len(tiles)*(size+13)
        overlay=Image.new('RGBA',(box_w,box_h));rr(overlay,(0,0,box_w-1,box_h-1),19,GREEN)
        for j,obj in enumerate(tiles):paste(overlay,obj,(box_w-obj.width)/2,18+j*(size+13))
        paste(im,overlay,(w-box_w)/2,1660 if portrait else 945,alpha)
    # Chapter timeline stays inside both social-media safe areas.
    d=ImageDraw.Draw(im);gap=14;available=w-2*margin;segment=(available-7*gap)/8
    yy=1828 if portrait else 1036
    for i in range(8):
        xx=margin+i*(segment+gap);rr(im,(xx,yy,xx+segment,yy+5),2,BORDER)
        q=1 if i<scene else (st/6 if i==scene else 0)
        if q>0:rr(im,(xx,yy,xx+segment*q,yy+5),2,GREEN)
    if t>47.5:im=Image.blend(im,Image.new('RGBA',(w,h),CREAM+(255,)),smooth((t-47.5)/.5))
    return im.convert('RGB')

def ffmpeg():
    import imageio_ffmpeg
    return imageio_ffmpeg.get_ffmpeg_exe()

def stills(mode):
    folder=ROOT/f'frames-{mode}';folder.mkdir(exist_ok=True)
    dims=(384,216) if mode=='landscape' else (216,384)
    sheet=Image.new('RGB',(dims[0]*2,dims[1]*4),CREAM)
    for i in range(8):
        im=frame(i*6+3.6,mode);im.save(folder/f'{i+1:02d}.jpg',quality=95)
        sheet.paste(im.resize(dims,Image.Resampling.LANCZOS),((i%2)*dims[0],(i//2)*dims[1]))
    sheet.save(ROOT/f'分镜预览-{mode}.jpg',quality=95)
    frame(3.5,mode).save(ROOT/('封面-竖屏.jpg' if mode=='portrait' else '封面-横屏.jpg'),quality=96)

def render(mode):
    w,h=(1080,1920) if mode=='portrait' else (1920,1080)
    cmd=[ffmpeg(),'-y','-f','rawvideo','-pix_fmt','rgb24','-s',f'{w}x{h}','-r',str(FPS),'-i','-',
         '-an','-c:v','libx264','-preset','fast','-crf','18','-pix_fmt','yuv420p','-movflags','+faststart',str(ROOT/f'visuals-{mode}.mp4')]
    start=time.monotonic()
    with (ROOT/f'render-{mode}.log').open('w') as log:
        p=subprocess.Popen(cmd,stdin=subprocess.PIPE,stdout=log,stderr=log)
        try:
            for n in range(round(FPS*DURATION)):
                p.stdin.write(frame(n/FPS,mode).tobytes())
                if n%(FPS*6)==0:print(f'{mode}: {n//FPS:02d}/{DURATION}s ({time.monotonic()-start:.1f}s)',flush=True)
            p.stdin.close()
            if p.wait():raise RuntimeError('Encoder failed; see render log')
        except BaseException:p.kill();p.wait();raise
    print('Rendered',mode,flush=True)

def mux(mode):
    suffix='竖屏' if mode=='portrait' else '横屏'
    out=ROOT/f'大疆4g模块辅助工具-宣传动画-{suffix}-配音版.mp4'
    audio=ROOT/'audio/mix_mastered.wav'
    if not audio.is_file():raise RuntimeError('Audio master not yet ready')
    with (ROOT/f'mux-{mode}.log').open('w') as log:
        subprocess.run([ffmpeg(),'-y','-i',str(ROOT/f'visuals-{mode}.mp4'),'-i',str(audio),
                        '-map','0:v:0','-map','1:a:0','-c:v','copy','-c:a','aac','-b:a','192k','-ar','48000',
                        '-t',str(DURATION),'-movflags','+faststart','-metadata','title=大疆4g模块辅助工具',
                        '-metadata','comment=中文合成配音与原创音乐；界面为示意，数据为示例。',str(out)],
                       check=True,stdout=log,stderr=log)
    print(out,flush=True)

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--format',choices=['landscape','portrait'],default='landscape')
    p.add_argument('--stills',action='store_true');p.add_argument('--mux',action='store_true')
    a=p.parse_args()
    if a.mux:mux(a.format)
    elif a.stills:stills(a.format)
    else:stills(a.format);render(a.format)
