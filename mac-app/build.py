#!/usr/bin/env python3
"""Build the self-contained menu-bar app without installing or launching it.

Requires the Xcode Command Line Tools, Go, pkg-config and libusb development files.
Run: python3 mac-app/build.py --output build/大疆4g模块辅助工具.app
The previous output is replaced only after compilation and signature checks pass.
"""

from __future__ import annotations

import argparse
import errno
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import tempfile


SOURCE_DIR = Path(__file__).resolve().parent
WORKSPACE = SOURCE_DIR.parent
APP_NAME = "大疆4g模块辅助工具"
EXECUTABLE_NAME = "DJI4GHelper"
BUNDLE_ID = "cn.wanlidecai.dji4g-helper"
RUNTIME_ITEMS = ("bin", "lib", "licenses", "LICENSE", "THIRD_PARTY_NOTICES.md")
LIBUSB_INSTALL_NAME = "@executable_path/../lib/libusb-1.0.0.dylib"


def run(*args: str | Path, capture: bool = False) -> str:
    result = subprocess.run(
        [str(arg) for arg in args],
        check=True,
        text=True,
        stdout=subprocess.PIPE if capture else None,
    )
    return result.stdout if capture else ""


def make_plist(minimum_macos: str = "13.0") -> dict:
    return {
        "CFBundleDevelopmentRegion": "zh_CN",
        "CFBundleDisplayName": APP_NAME,
        "CFBundleExecutable": EXECUTABLE_NAME,
        "CFBundleIconFile": "AppIcon",
        "CFBundleIdentifier": BUNDLE_ID,
        "CFBundleInfoDictionaryVersion": "6.0",
        "CFBundleName": APP_NAME,
        "CFBundlePackageType": "APPL",
        "CFBundleShortVersionString": "1.2.2",
        "CFBundleVersion": "5",
        "LSMinimumSystemVersion": minimum_macos,
        "LSUIElement": True,
        "NSHighResolutionCapable": True,
        "NSPrincipalClass": "NSApplication",
        "NSAppTransportSecurity": {
            "NSAllowsArbitraryLoads": False,
            "NSAllowsArbitraryLoadsInWebContent": False,
            # macOS 13 permits direct IP loads; macOS 14+ supports this
            # specific IP exception. ATS cannot restrict a port: the app's
            # WKNavigationDelegate restricts the actual management URL.
            "NSExceptionDomains": {
                "127.0.0.1": {
                    "NSIncludesSubdomains": False,
                    "NSExceptionAllowsInsecureHTTPLoads": True,
                },
            },
        },
    }


def validate_runtime(runtime: Path) -> None:
    backend = runtime / "bin/djonehub-macos"
    libusb = runtime / "lib/libusb-1.0.0.dylib"
    for binary in (backend, libusb):
        if not binary.is_file():
            raise FileNotFoundError(binary)
        if "arm64" not in run("lipo", "-archs", binary, capture=True):
            raise RuntimeError(f"Runtime binary lacks arm64 architecture: {binary}")
    if LIBUSB_INSTALL_NAME not in run("otool", "-L", backend, capture=True):
        raise RuntimeError("Runtime backend does not use the expected relative libusb path")
    if LIBUSB_INSTALL_NAME not in run("otool", "-D", libusb, capture=True):
        raise RuntimeError("Runtime libusb install name is not relative to the backend")
    backend.chmod(backend.stat().st_mode | 0o111)


def minimum_macos_version(*binaries: Path) -> str:
    """The local libusb installation may require a newer macOS than the shell."""
    versions = [(13, 0)]
    for binary in binaries:
        metadata = run("otool", "-l", binary, capture=True)
        for value in re.findall(r"\bminos\s+(\d+(?:\.\d+)+)", metadata):
            versions.append(tuple(int(part) for part in value.split(".")))
        for section in metadata.split("Load command"):
            if "cmd LC_VERSION_MIN_MACOSX" in section:
                match = re.search(r"\bversion\s+(\d+(?:\.\d+)+)", section)
                if match:
                    versions.append(tuple(int(part) for part in match.group(1).split(".")))
    return ".".join(str(part) for part in max(versions))


def copy_runtime(source: Path, destination: Path) -> None:
    missing = [item for item in RUNTIME_ITEMS if not (source / item).exists()]
    if missing:
        raise FileNotFoundError(f"Runtime resources missing: {', '.join(missing)}")
    destination.mkdir(parents=True)
    for item in RUNTIME_ITEMS:
        src, dst = source / item, destination / item
        if src.is_dir():
            shutil.copytree(src, dst)
        else:
            shutil.copy2(src, dst)
    validate_runtime(destination)


def create_runtime(destination: Path, backend_source: Path, license_source: Path | None) -> None:
    """Bundle the local libusb installation without needing a previous release."""
    pkg_config = shutil.which("pkg-config")
    if pkg_config is None:
        raise FileNotFoundError("pkg-config is required; install pkg-config and libusb first")
    libdir = Path(run(pkg_config, "--variable=libdir", "libusb-1.0", capture=True).strip())
    prefix = Path(run(pkg_config, "--variable=prefix", "libusb-1.0", capture=True).strip())
    version = run(pkg_config, "--modversion", "libusb-1.0", capture=True).strip()
    library = libdir / "libusb-1.0.0.dylib"
    if not library.is_file():
        raise FileNotFoundError(f"pkg-config libusb installation has no macOS dynamic library: {library}")
    license_source = license_source or prefix / "COPYING"
    if not license_source.is_file():
        raise FileNotFoundError(
            f"libusb license not found: {license_source}; supply its full license with --libusb-license"
        )
    backend_license = backend_source / "LICENSE"
    if not backend_license.is_file():
        raise FileNotFoundError(backend_license)
    for item in ("bin", "lib", "licenses"):
        (destination / item).mkdir(parents=True)
    bundled_library = destination / "lib/libusb-1.0.0.dylib"
    shutil.copy2(library, bundled_library)
    # Homebrew libraries may be read-only; only our new copy needs metadata and
    # install-name updates before it is signed.
    bundled_library.chmod(bundled_library.stat().st_mode | 0o200)
    run("install_name_tool", "-id", LIBUSB_INSTALL_NAME, bundled_library)
    shutil.copy2(license_source, destination / "licenses/libusb-COPYING")
    shutil.copy2(backend_license, destination / "LICENSE")
    (destination / "THIRD_PARTY_NOTICES.md").write_text(
        "# Third-Party Notices\n\n"
        "The shared DJOneHub backend contains code derived from VoHive. "
        "Its full license and required notice are retained in `LICENSE`.\n\n"
        "Required Notice: Copyright iniwex5 (https://github.com/iniwex5/vohive)\n\n"
        f"This build bundles libusb {version}, distributed under the GNU Lesser General "
        "Public License, version 2.1 or later.\n\n"
        "- Project: <https://libusb.info/>\n"
        f"- Source: <https://github.com/libusb/libusb/releases/tag/v{version}>\n"
        "- Full license text: `licenses/libusb-COPYING`\n\n"
        "Source dependency licenses and notices are retained in the shared backend's "
        "`third_party/` directories and `THIRD_PARTY_NOTICES.md` in the source repository.\n",
        encoding="utf-8",
    )


def sign_and_verify(app: Path) -> None:
    # Desktop/iCloud Finder metadata can be inherited by a new compiler output.
    # Clear metadata only on our newly created bundle, never on the source runtime.
    run("xattr", "-cr", app)
    runtime = app / "Contents/Resources/runtime"
    # Sign Mach-O dependencies before their consumer and enclosing bundle.
    for library in sorted((runtime / "lib").rglob("*.dylib")):
        run("codesign", "--force", "--sign", "-", "--timestamp=none", library)
    for binary in sorted((runtime / "bin").iterdir()):
        if binary.is_file():
            run("codesign", "--force", "--sign", "-", "--timestamp=none", binary)
    run(
        "codesign", "--force", "--sign", "-", "--timestamp=none",
        app / "Contents/MacOS" / EXECUTABLE_NAME,
    )
    run("codesign", "--force", "--sign", "-", "--timestamp=none", app)
    run("codesign", "--verify", "--deep", "--strict", app)


def replace_output(staged_app: Path, output: Path, build_dir: Path) -> None:
    if output.is_symlink():
        raise RuntimeError(f"Refusing to replace a symlink: {output}")
    if output.exists() and not output.is_dir():
        raise RuntimeError(f"App output is not a directory: {output}")
    backup = None
    if output.exists():
        backup_root = Path(tempfile.mkdtemp(prefix="previous-", dir=build_dir))
        backup = backup_root / output.name
        os.replace(output, backup)
    try:
        try:
            os.replace(staged_app, output)
        except OSError as error:
            if error.errno != errno.EXDEV:
                raise
            # A separately mounted output directory cannot use an atomic rename.
            # Retain the previous app until this complete copy is verified.
            shutil.copytree(staged_app, output)
        run("xattr", "-cr", output)
        run("codesign", "--verify", "--deep", "--strict", output)
    except BaseException:
        if output.exists():
            shutil.rmtree(output)
        if backup is not None:
            os.replace(backup, output)
        raise
    finally:
        if backup is not None and not backup.exists():
            backup.parent.rmdir()
    if backup is not None:
        shutil.rmtree(backup.parent)


def build(runtime_source: Path | None, go: str, backend_source: Path, output: Path | None = None,
          libusb_license: Path | None = None) -> Path:
    main_source = SOURCE_DIR / "main.swift"
    icon_source = SOURCE_DIR / "make-icon.swift"
    if not main_source.is_file() or not icon_source.is_file():
        raise FileNotFoundError("main.swift and make-icon.swift must be beside build.py")
    build_dir = SOURCE_DIR / "build"
    build_dir.mkdir(exist_ok=True)
    output = output or WORKSPACE / f"{APP_NAME}.app"
    output.parent.mkdir(parents=True, exist_ok=True)
    # Compile and sign outside Desktop/iCloud so Finder metadata cannot taint
    # newly generated Mach-O files. Only the verified result enters the workspace.
    with tempfile.TemporaryDirectory(prefix="dji4g-build-") as stage_name:
        stage = Path(stage_name)
        app = stage / output.name
        contents = app / "Contents"
        macos = contents / "MacOS"
        resources = contents / "Resources"
        macos.mkdir(parents=True)
        resources.mkdir()
        with (contents / "Info.plist").open("wb") as handle:
            plistlib.dump(make_plist(), handle, sort_keys=True)
        (contents / "PkgInfo").write_bytes(b"APPL????")
        if runtime_source is None:
            create_runtime(resources / "runtime", backend_source, libusb_license)
        else:
            copy_runtime(runtime_source, resources / "runtime")
        environment = dict(os.environ, GOOS="darwin", GOARCH="arm64", CGO_ENABLED="1",
                           MACOSX_DEPLOYMENT_TARGET="13.0")
        for flag_name in ("CGO_CFLAGS", "CGO_LDFLAGS"):
            environment[flag_name] = environment.get(flag_name, "-O2 -g") + " -mmacosx-version-min=13.0"
        backend = resources / "runtime/bin/djonehub-macos"
        subprocess.run([go, "build", "-trimpath", "-ldflags=-s -w", "-o", str(backend),
                        "./cmd/djonehub-macos"], cwd=backend_source, env=environment, check=True)
        # Keep the rebuilt backend relocatable, using the bundled libusb.
        dependencies = run("otool", "-L", backend, capture=True)
        for line in dependencies.splitlines()[1:]:
            name = line.strip().split(" (", 1)[0]
            if "libusb" in name and name.startswith("/"):
                run("install_name_tool", "-change", name,
                    LIBUSB_INSTALL_NAME, backend)
        validate_runtime(resources / "runtime")
        minimum_macos = minimum_macos_version(backend, resources / "runtime/lib/libusb-1.0.0.dylib")
        with (contents / "Info.plist").open("wb") as handle:
            plistlib.dump(make_plist(minimum_macos), handle, sort_keys=True)
        run(
            "xcrun", "swiftc", "-O", "-target", "arm64-apple-macos13.0",
            "-framework", "AppKit", "-framework", "CoreGraphics",
            icon_source, "-o", stage / "make-icon",
        )
        iconset = stage / "AppIcon.iconset"
        run(stage / "make-icon", iconset)
        run("iconutil", "-c", "icns", iconset, "-o", resources / "AppIcon.icns")
        run(
            "xcrun", "swiftc", "-O", "-target", "arm64-apple-macos13.0",
            "-framework", "AppKit", "-framework", "WebKit",
            "-framework", "ServiceManagement", main_source,
            SOURCE_DIR / "WakeRecovery.swift", SOURCE_DIR / "NetworkRecovery.swift",
            SOURCE_DIR / "Notifications.swift", "-framework", "UserNotifications",
            "-o", macos / EXECUTABLE_NAME,
        )
        run("plutil", "-lint", contents / "Info.plist")
        sign_and_verify(app)
        # Preserve a full-resolution preview for future icon review.
        shutil.copy2(iconset / "icon_512x512@2x.png", build_dir / "AppIcon-preview.png")
        replace_output(app, output, build_dir)
    return output


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--runtime-source", type=Path,
        help="Optional existing app's Contents/Resources/runtime directory (copied, never modified); default: bundle local pkg-config libusb",
    )
    parser.add_argument("--go", default=shutil.which("go"), help="Go executable; defaults to go on PATH")
    parser.add_argument("--libusb-license", type=Path, help="Full libusb COPYING file; defaults to pkg-config prefix/COPYING")
    parser.add_argument("--backend-source", type=Path, default=WORKSPACE / "windows-app/backend")
    parser.add_argument("--output", type=Path, help="App output path; use a local directory outside Desktop/iCloud if Finder metadata interferes")
    args = parser.parse_args()
    if not args.go:
        parser.error("Go was not found on PATH; install Go or supply --go /path/to/go")
    go = shutil.which(str(Path(args.go).expanduser()))
    if go is None:
        parser.error(f"Go executable was not found or is not executable: {args.go}")
    output = build(args.runtime_source.expanduser().resolve() if args.runtime_source else None,
                   go, args.backend_source.expanduser().resolve(),
                   args.output.expanduser().resolve() if args.output else None,
                   args.libusb_license.expanduser().resolve() if args.libusb_license else None)
    print(f"Built and verified: {output}")


if __name__ == "__main__":
    main()
