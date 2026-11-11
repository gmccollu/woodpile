#!/usr/bin/env python3
"""Generate THIRD_PARTY_NOTICES.txt: every third-party component that ships inside Woodpile.

    scripts/third_party_notices.py            # rewrite THIRD_PARTY_NOTICES.txt
    scripts/third_party_notices.py --check    # exit 1 if the committed file is stale (CI)

What it covers:
  * Go modules compiled into the woodpile binary, on every release target
    (linux, windows, darwin x amd64, arm64; CGO_ENABLED=0). The module set comes from
    `go list -deps ./cmd/woodpile`; license names from go-licenses; the texts are every
    LICENSE / COPYING / NOTICE / PATENTS file in the module's root, verbatim.
  * The Go standard library and runtime, which are linked into every Go binary.
  * npm packages bundled into the web UI. Vite apps list runtime packages under
    devDependencies, so the set is taken from what the build actually bundles: a
    sourcemapped `vite build` of a temporary copy of web/ (JS) plus packages that
    stamp a "/*! name vX | License" banner into the output (CSS). License names come
    from license-checker-rseidelsohn (via npx).

It fails (exit 2) if any component's license isn't on the permissive allowlist below,
or can't be identified, so a new copyleft or unknown dependency can't slip in.

Needs: go, node/npm (npx), network access for `go run` / `npx` on first use.
The output is deterministic (sorted, no timestamps).
"""

import argparse
import difflib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

GO_LICENSES = os.environ.get("GO_LICENSES", "go run github.com/google/go-licenses@v1.6.0").split()
LICENSE_CHECKER = ["npx", "--yes", "license-checker-rseidelsohn@5.0.1"]

TARGETS = [(os_, arch) for os_ in ("linux", "windows", "darwin") for arch in ("amd64", "arm64")]

# Licenses that allow redistribution in binary form with attribution only.
PERMISSIVE = {
    "MIT", "MIT-0", "BSD-2-Clause", "BSD-3-Clause", "0BSD", "Apache-2.0", "ISC", "Zlib",
    "BSL-1.0", "Unlicense", "CC0-1.0", "blessing",
}

# Classifier misses, each checked by reading the license file. Key: module path.
OVERRIDES = {
    # LICENSE is the standard BSD 3-clause text; go-licenses v1.6.0 reports "Unknown".
    "modernc.org/mathutil": "BSD-3-Clause",
    # Its own LICENSE is BSD-3-Clause; LICENSE-3RD-PARTY.md covers the transpiled musl libc,
    # go-netdb and nixpkgs code (MIT). go-licenses reports only "MIT" from the second file.
    "modernc.org/libc": "BSD-3-Clause AND MIT",
    # BSD-3-Clause, plus SQLite itself (public domain, SPDX "blessing") and sqlite-vec (MIT),
    # per the module's LICENSE-SQLITE, LICENSE-SQLITE_VEC and LICENSE-3RD-PARTY.md.
    "modernc.org/sqlite": "BSD-3-Clause AND blessing AND MIT",
}

LICENSE_FILE = re.compile(r"^(licen[cs]e|copying|notice|copyright|patents|unlicense)([._-].*)?$", re.I)
BANNER = re.compile(r"/\*!\s*(@?[a-z0-9][\w.-]*(?:/[\w.-]+)?)\s+v(\d[\w.+-]*)\s*\|\s*([\w.+-]+) License", re.I)

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "THIRD_PARTY_NOTICES.txt"
RULE = "=" * 78
SUBRULE = "-" * 78


def run(cmd, cwd=ROOT, env=None, capture=True):
    e = dict(os.environ)
    if env:
        e.update(env)
    r = subprocess.run(cmd, cwd=cwd, env=e, text=True,
                       stdout=subprocess.PIPE if capture else None,
                       stderr=subprocess.PIPE if capture else None)
    if r.returncode != 0:
        sys.stderr.write(f"$ {' '.join(cmd)}\n{r.stderr or ''}")
        raise SystemExit(f"third_party_notices: command failed ({r.returncode})")
    return r.stdout


def clean_text(raw: bytes) -> str:
    text = raw.decode("utf-8", errors="replace").replace("\r\n", "\n").replace("\r", "\n")
    lines = [line.rstrip() for line in text.split("\n")]
    while lines and not lines[-1]:
        lines.pop()
    while lines and not lines[0]:
        lines.pop(0)
    return "\n".join(lines) + "\n"


def license_files(directory: Path):
    files = sorted((p for p in directory.iterdir() if p.is_file() and LICENSE_FILE.match(p.name)),
                   key=lambda p: p.name.lower())
    return [(p.name, clean_text(p.read_bytes())) for p in files]


def go_components():
    fmt = ("{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t"
           "{{if .Replace}}{{.Replace.Dir}}{{else}}{{.Dir}}{{end}}{{end}}{{end}}")
    mods = {}
    names = {}
    for goos, goarch in TARGETS:
        env = {"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0", "GOFLAGS": "-mod=readonly"}
        for line in run(["go", "list", "-deps", "-f", fmt, "./cmd/woodpile"], env=env).splitlines():
            if line.strip():
                path, version, directory = line.split("\t")
                mods[path] = (version, Path(directory))
        for line in run(GO_LICENSES + ["report", "./cmd/woodpile"], env=env).splitlines():
            parts = line.strip().split(",")
            if len(parts) == 3:
                names[parts[0]] = parts[2]
    comps = []
    for path in sorted(mods):
        version, directory = mods[path]
        lic = OVERRIDES.get(path) or names.get(path, "Unknown")
        texts = license_files(directory)
        if not texts:
            raise SystemExit(f"third_party_notices: no license file in {directory} ({path})")
        comps.append({"name": path, "version": version, "license": lic, "texts": texts})
    return comps


def go_stdlib(go_comps):
    goroot = Path(run(["go", "env", "GOROOT"]).strip())
    lic = goroot / "LICENSE"
    if lic.exists():
        text = clean_text(lic.read_bytes())
    else:
        # Some Go distributions (Homebrew) drop GOROOT/LICENSE. golang.org/x/sys, which is
        # always compiled in, carries the identical text (checked against go1.27.4).
        xsys = [c for c in go_comps if c["name"] == "golang.org/x/sys"]
        if not xsys:
            raise SystemExit("third_party_notices: no GOROOT/LICENSE and no golang.org/x/sys to copy it from")
        text = dict(xsys[0]["texts"])["LICENSE"]
    return {"name": "Go standard library and runtime", "version": "", "license": "BSD-3-Clause",
            "texts": [("LICENSE", text)]}


def bundled_npm_packages(webcopy: Path):
    pkgs = set()
    assets = webcopy / "dist"
    for mapfile in sorted(assets.rglob("*.map")):
        for src in json.loads(mapfile.read_text()).get("sources", []):
            m = re.search(r"node_modules/((?:@[^/]+/)?[^/]+)/", src)
            if m:
                pkgs.add(m.group(1))
    for f in sorted(assets.rglob("*")):
        if f.suffix in (".css", ".js") and f.is_file():
            for m in BANNER.finditer(f.read_text(errors="replace")):
                if (webcopy / "node_modules" / m.group(1) / "package.json").exists():
                    pkgs.add(m.group(1))
    return sorted(pkgs)


def npm_components():
    web = ROOT / "web"
    if not (web / "package.json").exists():
        return []
    tmp = Path(tempfile.mkdtemp(prefix="woodpile-notices-"))
    try:
        webcopy = tmp / "web"
        shutil.copytree(web, webcopy, ignore=shutil.ignore_patterns("node_modules", "dist"))
        (webcopy / "dist").mkdir()
        run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error"], cwd=webcopy)
        run(["npx", "vite", "build", "--sourcemap", "--logLevel", "error"], cwd=webcopy)
        pkgs = bundled_npm_packages(webcopy)
        if not pkgs:
            return []
        info = json.loads(run(LICENSE_CHECKER + ["--json", "--start", ".", "--includePackages", ";".join(pkgs)],
                              cwd=webcopy))
        comps = []
        for key in sorted(info):
            name, _, version = key.rpartition("@")
            if name not in pkgs:
                continue
            lic = info[key].get("licenses", "Unknown")
            if isinstance(lic, list):
                lic = " OR ".join(lic)
            texts = license_files(Path(info[key]["path"]))
            if not texts:
                raise SystemExit(f"third_party_notices: no license file for {key}")
            comps.append({"name": name, "version": version, "license": lic, "texts": texts})
        missing = set(pkgs) - {c["name"] for c in comps}
        if missing:
            raise SystemExit(f"third_party_notices: license-checker didn't report {sorted(missing)}")
        return comps
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def permissive(expr: str) -> bool:
    # "A OR B" is fine if either choice is permissive; "A AND B" needs both.
    expr = expr.strip("() ")
    if " OR " in expr:
        return any(permissive(p) for p in expr.split(" OR "))
    if " AND " in expr:
        return all(permissive(p) for p in expr.split(" AND "))
    return expr in PERMISSIVE


def render(go, std, npm):
    out = []
    w = out.append
    w("Woodpile third-party notices")
    w(RULE)
    w("")
    w("Woodpile itself is released under the MIT License (see LICENSE). The woodpile")
    w("program and its built-in web UI also contain the third-party software listed")
    w("below, each under its own license, reproduced in full after the summary.")
    w("")
    w("This file is generated by scripts/third_party_notices.py; don't edit it by hand.")
    w("")
    w("Go modules compiled into the woodpile program (all platforms):")
    w("")
    for c in [std] + go:
        w(f"  {c['name']:<44} {c['version']:<40} {c['license']}".rstrip())
    w("")
    w("Packages bundled into the web UI:")
    w("")
    for c in npm:
        w(f"  {c['name']:<44} {c['version']:<40} {c['license']}".rstrip())
    w("")
    for c in [std] + go + npm:
        w(RULE)
        title = f"{c['name']} {c['version']}".strip()
        w(f"{title} ({c['license']})")
        for fname, text in c["texts"]:
            w(SUBRULE + f" {fname}")
            w("")
            out.append(text.rstrip("\n"))
            w("")
    return "\n".join(out).rstrip("\n") + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--check", action="store_true", help="fail if THIRD_PARTY_NOTICES.txt is stale")
    ap.add_argument("--output", type=Path, default=OUT)
    args = ap.parse_args()

    go = go_components()
    std = go_stdlib(go)
    npm = npm_components()

    flagged = [c for c in go + npm if not permissive(c["license"])]
    for c in flagged:
        sys.stderr.write(f"NOT PERMISSIVE OR UNKNOWN: {c['name']} {c['version']}: {c['license']}\n")

    text = render(go, std, npm)
    if args.check:
        current = args.output.read_text() if args.output.exists() else ""
        if current != text:
            diff = difflib.unified_diff(current.splitlines(), text.splitlines(),
                                        str(args.output), "regenerated", lineterm="", n=1)
            sys.stdout.write("\n".join(list(diff)[:80]) + "\n")
            sys.stderr.write(f"{args.output.name} is stale: run scripts/third_party_notices.py and commit it.\n")
            return 1
        print(f"{args.output.name} is up to date ({len(go)} Go modules, {len(npm)} UI packages).")
    else:
        args.output.write_text(text)
        print(f"wrote {args.output} ({len(go)} Go modules, {len(npm)} UI packages)")
    return 2 if flagged else 0


if __name__ == "__main__":
    sys.exit(main())
