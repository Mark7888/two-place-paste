#!/usr/bin/env node
// Writes the third-party notices file a TwoPlacePaste build ships with.
//
// MIT, BSD, ISC and Apache-2.0 all ask that a binary distribution carry the
// copyright notice and license text of the code it contains. Every build that
// leaves this repository — the relay's Docker image, the desktop app, the
// Android app — runs this script and ships its output, so the list is always
// the one the build actually used rather than one kept by hand.
//
//   node scripts/notices/notices.mjs --title TITLE --intro TEXT --out FILE
//        [--go DIR=PKG[,PKG...]] [--goos OS[,OS...]] [--go-optional]
//        [--npm DIR]... [--maven FILE]
//
// --go       a Go module directory and the packages to list (e.g.
//            server=./cmd/tpp). Every module the packages compile in is
//            listed, plus the Go standard library and runtime.
// --goos     the target operating systems; the union of their dependencies is
//            listed. Defaults to the host's.
// --go-optional
//            skip the Go part with a warning when there is no `go` on PATH
//            (the desktop UI's build also runs in CI jobs that have no Go).
// --npm      an npm project; every package in its lockfile that is not a dev
//            dependency is listed. Run `npm ci` first.
// --maven    a JSON file of Maven artefacts written by the Android build:
//            [{"coordinates": "g:a:v", "licenses": [{"name", "url"}]}].
//            POMs carry a license's name, not its text, so the full text of
//            each license named there is appended once (scripts/notices/licenses).
//
// Nothing but the Node standard library is used, so it runs wherever a build
// already has Node: the desktop UI build, the Android build and the Docker
// build stage.

import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const OWN_MODULE = "github.com/Mark7888/two-place-paste";
const RULE = "=".repeat(80);
const LICENSE_FILE = /^(licen[cs]e|copying|unlicense)([._-].*)?$/i;
const NOTICE_FILE = /^(notice|patents)([._-].*)?$/i;
const HERE = dirname(fileURLToPath(import.meta.url));

function fail(message) {
  console.error(`notices: ${message}`);
  process.exit(1);
}

function parseArgs(argv) {
  const opts = { go: [], goos: [], goOptional: false, npm: [], maven: null, title: "", intro: "", out: "" };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    const value = () => {
      if (i + 1 >= argv.length) fail(`${flag} needs a value`);
      return argv[++i];
    };
    switch (flag) {
      case "--go": {
        const [dir, pkgs] = value().split("=");
        if (!dir || !pkgs) fail("--go takes DIR=PKG[,PKG...]");
        opts.go.push({ dir: resolve(dir), pkgs: pkgs.split(",") });
        break;
      }
      case "--goos":
        opts.goos = value().split(",").filter(Boolean);
        break;
      case "--go-optional":
        opts.goOptional = true;
        break;
      case "--npm":
        opts.npm.push(resolve(value()));
        break;
      case "--maven":
        opts.maven = resolve(value());
        break;
      case "--title":
        opts.title = value();
        break;
      case "--intro":
        opts.intro = value();
        break;
      case "--out":
        opts.out = resolve(value());
        break;
      default:
        fail(`unknown flag ${flag}`);
    }
  }
  if (!opts.title || !opts.out) fail("--title and --out are required");
  return opts;
}

// licenseFiles returns the license files first and the NOTICE/PATENTS files
// after them, each as {name, text}.
function licenseFiles(dir) {
  const names = readdirSync(dir).filter((n) => statSync(join(dir, n)).isFile());
  const pick = (re) => names.filter((n) => re.test(n)).sort();
  return [...pick(LICENSE_FILE), ...pick(NOTICE_FILE)].map((name) => ({
    name,
    text: readFileSync(join(dir, name), "utf8").replace(/\r\n/g, "\n").trimEnd(),
  }));
}

// guessLicense names the license of a Go module from its text, for the table
// of contents only. It answers only for texts it recognises outright; the
// full text that follows is what counts.
function guessLicense(text) {
  if (/Mozilla Public License,? [Vv]ersion 2\.0/.test(text)) return "MPL-2.0";
  if (/Apache License/.test(text) && /Version 2\.0/.test(text)) return "Apache-2.0";
  if (/Permission is hereby granted, free of charge/.test(text)) return "MIT";
  if (/Permission to use, copy, modify, and(\/or)? distribute this software/.test(text)) return "ISC";
  if (/Redistribution and use in source and binary forms/.test(text)) {
    return /Neither the name|name of the copyright holder/i.test(text) ? "BSD-3-Clause" : "BSD-2-Clause";
  }
  return "";
}

function hasGo() {
  try {
    execFileSync("go", ["version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function goComponents(opts) {
  if (opts.go.length === 0) return [];
  if (!hasGo()) {
    if (opts.goOptional) {
      console.warn("notices: no `go` on PATH; the Go modules are left out of this file");
      return [];
    }
    fail("no `go` on PATH");
  }
  // Asked in the module's own directory: its go.mod or go.work can select a
  // toolchain other than the one this script was started next to, and that
  // toolchain's standard library is the one the binary carries.
  const goEnv = (name) =>
    execFileSync("go", ["env", name], { cwd: opts.go[0].dir, encoding: "utf8" }).trim();
  const oses = opts.goos.length > 0 ? opts.goos : [goEnv("GOOS")];
  const modules = new Map();
  for (const { dir, pkgs } of opts.go) {
    for (const goos of oses) {
      // CGO_ENABLED=1 so files behind a cgo build tag count too: the desktop
      // app's macOS tray is cgo. Listing never compiles, so no C toolchain is
      // needed for it.
      const out = execFileSync(
        "go",
        ["list", "-deps", "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}\t{{.Main}}{{end}}", ...pkgs],
        { cwd: dir, encoding: "utf8", env: { ...process.env, GOOS: goos, CGO_ENABLED: "1" } },
      );
      for (const line of out.split("\n")) {
        const [path, version, modDir, main] = line.split("\t");
        if (!path || main === "true" || path === OWN_MODULE || path.startsWith(`${OWN_MODULE}/`)) continue;
        if (!modDir) fail(`module ${path}@${version} is not downloaded; run go mod download`);
        modules.set(path, { path, version, dir: modDir });
      }
    }
  }

  const components = [];
  const goroot = goEnv("GOROOT");
  components.push({
    name: "Go standard library and runtime",
    version: goEnv("GOVERSION"),
    source: "https://go.dev",
    license: "BSD-3-Clause",
    files: licenseFiles(goroot),
  });
  for (const mod of [...modules.values()].sort((a, b) => a.path.localeCompare(b.path))) {
    const files = licenseFiles(mod.dir);
    if (!files.some((f) => LICENSE_FILE.test(f.name))) {
      fail(`Go module ${mod.path}@${mod.version} has no license file at its root`);
    }
    components.push({
      name: mod.path,
      version: mod.version,
      source: `https://pkg.go.dev/${mod.path}@${mod.version}`,
      license: guessLicense(files[0].text),
      files,
    });
  }
  return components;
}

function npmComponents(dir) {
  const lockPath = join(dir, "package-lock.json");
  if (!existsSync(lockPath)) fail(`${lockPath} does not exist`);
  const lock = JSON.parse(readFileSync(lockPath, "utf8"));
  if (!lock.packages) fail(`${lockPath} is an old lockfile format with no "packages"`);

  const seen = new Map();
  for (const [key, entry] of Object.entries(lock.packages)) {
    // "" is the project itself; dev and devOptional never reach a build; a
    // link points at a workspace member, which is our own code.
    if (key === "" || entry.dev || entry.devOptional || entry.link) continue;
    const pkgDir = join(dir, key);
    if (!existsSync(pkgDir)) {
      // An optional package for another platform is not installed here.
      if (entry.optional) continue;
      fail(`${key} is in the lockfile but not installed; run npm ci in ${dir}`);
    }
    const manifest = JSON.parse(readFileSync(join(pkgDir, "package.json"), "utf8"));
    const name = manifest.name ?? key.slice(key.lastIndexOf("node_modules/") + "node_modules/".length);
    const version = manifest.version ?? entry.version ?? "";
    const id = `${name}@${version}`;
    if (seen.has(id)) continue;

    let license = manifest.license ?? entry.license ?? "";
    if (typeof license === "object") license = license.type ?? "";
    const files = licenseFiles(pkgDir);
    if (!files.some((f) => LICENSE_FILE.test(f.name))) {
      if (!license) fail(`npm package ${id} has neither a license file nor a license field`);
      files.unshift({
        name: "",
        text: `This package ships no license file. Its package.json declares the license ${license}; see https://spdx.org/licenses/ for the text.`,
      });
    }
    seen.set(id, {
      name,
      version,
      source: `https://www.npmjs.com/package/${name}/v/${version}`,
      license,
      files,
    });
  }
  return [...seen.values()].sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version));
}

// canonicalId maps a license as a POM names it to the SPDX id of a full text
// this script carries, or "" when it carries none.
function canonicalId(name, url) {
  const s = `${name} ${url}`;
  if (/apache/i.test(s) && /2/.test(s)) return "Apache-2.0";
  return "";
}

function mavenComponents(file) {
  const artefacts = JSON.parse(readFileSync(file, "utf8"));
  const components = [];
  const texts = new Set();
  for (const a of artefacts.sort((x, y) => x.coordinates.localeCompare(y.coordinates))) {
    const [group, artifact, version] = a.coordinates.split(":");
    // A library's POM is the only place its license is recorded. One that
    // declares none is still listed, with that said, rather than failing the
    // build over metadata this repository cannot fix.
    if (!a.licenses || a.licenses.length === 0) {
      console.warn(`notices: ${a.coordinates} declares no license in its POM or any parent POM`);
      components.push({
        name: `${group}:${artifact}`,
        version,
        source: `https://mvnrepository.com/artifact/${group}/${artifact}/${version}`,
        license: "",
        files: [{ name: "", text: "Its POM declares no license. See the project's own page for its terms." }],
      });
      continue;
    }
    const lines = a.licenses.map((l) => {
      const id = canonicalId(l.name ?? "", l.url ?? "");
      if (id) texts.add(id);
      return `Licensed under ${l.name ?? "an unnamed license"}${l.url ? ` (${l.url})` : ""}.` +
        (id ? ` The full text of ${id} is at the end of this file.` : "");
    });
    components.push({
      name: `${group}:${artifact}`,
      version,
      source: `https://mvnrepository.com/artifact/${group}/${artifact}/${version}`,
      license: a.licenses.map((l) => canonicalId(l.name ?? "", l.url ?? "") || l.name || "").join(", "),
      files: [{ name: "", text: lines.join("\n") }],
    });
  }
  const appendix = [...texts].sort().map((id) => ({
    name: `${id} (full text)`,
    version: "",
    source: "",
    license: id,
    files: [{ name: "", text: readFileSync(join(HERE, "licenses", `${id}.txt`), "utf8").trimEnd() }],
  }));
  return { components, appendix };
}

function render(opts, groups) {
  const out = [];
  out.push(opts.title, "=".repeat(opts.title.length), "");
  if (opts.intro) out.push(opts.intro, "");
  const all = groups.flatMap((g) => g.components);
  out.push(`This file lists the ${all.length} third-party components this build includes, each with its license text.`, "");
  for (const g of groups) {
    if (g.components.length === 0) continue;
    out.push(`${g.heading}:`);
    for (const c of g.components) {
      out.push(`  - ${c.name}${c.version ? ` ${c.version}` : ""}${c.license ? ` (${c.license})` : ""}`);
    }
    out.push("");
  }
  for (const c of all) {
    out.push(RULE, `${c.name}${c.version ? ` ${c.version}` : ""}`);
    if (c.license) out.push(`License: ${c.license}`);
    if (c.source) out.push(`Source: ${c.source}`);
    out.push(RULE, "");
    for (const f of c.files) {
      if (f.name && c.files.length > 1) out.push(`--- ${f.name} ---`, "");
      out.push(f.text, "");
    }
  }
  return out.join("\n");
}

const opts = parseArgs(process.argv.slice(2));
const groups = [];
const go = goComponents(opts);
if (go.length > 0) groups.push({ heading: "Go modules", components: go });
for (const dir of opts.npm) {
  groups.push({ heading: `npm packages (${basename(dir)})`, components: npmComponents(dir) });
}
if (opts.maven) {
  const { components, appendix } = mavenComponents(opts.maven);
  groups.push({ heading: "Android libraries", components });
  if (appendix.length > 0) groups.push({ heading: "License texts", components: appendix });
}
writeFileSync(opts.out, render(opts, groups));
console.log(`notices: wrote ${opts.out}`);
