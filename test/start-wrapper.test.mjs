import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile, chmod } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const runRoot = path.join(repoRoot, 'run');
const shellSpecs = [
  {
    name: 'POSIX shell',
    executable: 'bash',
    script: path.join(runRoot, 'start.sh'),
    bashPath: 'C:\\Program Files\\Git\\bin\\bash.exe',
    available: spawnSync('C:\\Program Files\\Git\\bin\\bash.exe', ['--version'], { stdio: 'ignore' }).status === 0,
    configureFakeDocker: async (binDir) => {
      const docker = path.join(binDir, 'docker');
      await writeFile(docker, `#!/bin/sh
printf '%s\\0' "$@" > "$CAPTURE_ARGS"
previous=
for argument in "$@"; do
  if [ "$previous" = --file ] || [ "$previous" = -f ]; then
    if [ -n "$CAPTURE_OVERRIDE" ]; then cp -- "$argument" "$CAPTURE_OVERRIDE"; fi
    previous=done
  else
    previous=$argument
  fi
done
exit "${'${FAKE_DOCKER_EXIT:-0}'}"
`);
      await chmod(docker, 0o755);
    },
    invoke: (script, args, options) => spawnSync('C:\\Program Files\\Git\\bin\\bash.exe', [script.replaceAll('\\', '/'), ...args], { encoding: 'utf8', ...options }),
  },
  {
    name: 'PowerShell',
    executable: 'pwsh',
    script: path.join(runRoot, 'start.ps1'),
    available: spawnSync('pwsh', ['-NoProfile', '-Command', '$PSVersionTable.PSVersion.ToString()'], { stdio: 'ignore' }).status === 0,
    configureFakeDocker: async (binDir) => {
      await writeFile(path.join(binDir, 'fake-docker.mjs'), `import { copyFileSync, writeFileSync } from 'node:fs';
const args = process.argv.slice(2);
writeFileSync(process.env.CAPTURE_ARGS, JSON.stringify(args));
let fileCount = 0;
for (let index = 0; index < args.length - 1; index++) {
  if ((args[index] === '--file' || args[index] === '-f') && process.env.CAPTURE_OVERRIDE && ++fileCount > 1) {
    try { copyFileSync(args[index + 1], process.env.CAPTURE_OVERRIDE); } catch {}
  }
}
process.exit(Number(process.env.FAKE_DOCKER_EXIT || 0));
`);
      await writeFile(path.join(binDir, 'docker.cmd'), `@echo off
node "%~dp0fake-docker.mjs" %*
exit /b %ERRORLEVEL%
`);
      await writeFile(path.join(binDir, 'docker'), `#!/bin/sh
exec node "$(dirname "$0")/fake-docker.mjs" "$@"
`);
      await chmod(path.join(binDir, 'docker'), 0o755);
    },
    invoke: (script, args, options) => spawnSync('pwsh', ['-NoProfile', '-File', script, ...args], { encoding: 'utf8', ...options }),
  },
];

async function withFixture(spec, { qrPresent = true, dockerExit = 0 } = {}, run) {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), 'tikuhelper-start-test-'));
  const fixtureRun = path.join(tempRoot, 'run');
  const binDir = path.join(tempRoot, 'bin');
  await mkdir(fixtureRun);
  await mkdir(binDir);
  const scriptName = path.basename(spec.script);
  const fixtureScript = path.join(fixtureRun, scriptName);
  await writeFile(fixtureScript, await readFile(spec.script));
  const composePath = path.join(fixtureRun, 'docker-compose.yml');
  const composeContents = 'services:\n  tikuhelper:\n    image: fixture\n';
  await writeFile(composePath, composeContents);
  const qrPath = path.join(fixtureRun, 'Wechat_qc_code.jpg');
  if (qrPresent) await writeFile(qrPath, 'fixture image');
  const argsCapture = path.join(tempRoot, 'args.json');
  const overrideCapture = path.join(tempRoot, 'override.yml');
  await spec.configureFakeDocker(binDir);

  try {
    await run({
      tempRoot,
      fixtureRun,
      fixtureScript,
      composePath,
      composeContents,
      qrPath,
      argsCapture,
      overrideCapture,
      env: {
        ...process.env,
        PATH: `${binDir}${path.delimiter}${process.env.PATH ?? ''}`,
        CAPTURE_ARGS: argsCapture,
        CAPTURE_OVERRIDE: overrideCapture,
        FAKE_DOCKER_EXIT: String(dockerExit),
      },
    });
  } finally {
    for (let attempt = 0; attempt < 20; attempt++) {
      try {
        await rm(tempRoot, { recursive: true, force: true });
        break;
      } catch (error) {
        if (error.code !== 'EBUSY' || attempt === 19) throw error;
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
    }
  }
}

for (const spec of shellSpecs) {
  test(`${spec.name} wrapper adds a read-only QR override and forwards arguments`, { skip: !spec.available }, async () => {
    await withFixture(spec, {}, async (fixture) => {
      const callerArgs = ['--profile', 'local profile', 'up', '-d'];
      const result = spec.invoke(fixture.fixtureScript, callerArgs, { cwd: fixture.fixtureRun, env: fixture.env });
      assert.equal(result.status, 0, result.stderr);

      const args = spec.name === 'POSIX shell'
        ? (await readFile(fixture.argsCapture)).toString().split('\0').filter(Boolean)
        : JSON.parse((await readFile(fixture.argsCapture, 'utf8')).replace(/^\uFEFF/, ''));
      assert.equal(args[0], 'compose');
      assert.equal(args[1], '--file');
      assert.ok(path.normalize(args[2]).endsWith(path.join('run', 'docker-compose.yml')));
      assert.deepEqual(args.slice(-callerArgs.length), callerArgs);

      const overrideIndex = args.findLastIndex((value, index) => index > 0 && (args[index - 1] === '--file' || args[index - 1] === '-f'));
      assert.notEqual(overrideIndex, -1);
      const overridePath = args[overrideIndex];
      const override = await readFile(fixture.overrideCapture, 'utf8');
      assert.match(override, /read_only:\s*true/);
      assert.match(override, /target:\s*\/usr\/share\/nginx\/html\/Wechat_qc_code\.jpg/);
      assert.match(override, /run[\\/]Wechat_qc_code\.jpg/);
      await assert.rejects(readFile(overridePath), { code: 'ENOENT' });
      assert.equal(await readFile(fixture.composePath, 'utf8'), fixture.composeContents);
    });
  });

  test(`${spec.name} wrapper omits the optional override when the QR image is absent`, { skip: !spec.available }, async () => {
    await withFixture(spec, { qrPresent: false }, async (fixture) => {
      const callerArgs = ['up', '-d'];
      const result = spec.invoke(fixture.fixtureScript, callerArgs, { cwd: fixture.fixtureRun, env: fixture.env });
      assert.equal(result.status, 0, result.stderr);
      const args = spec.name === 'POSIX shell'
        ? (await readFile(fixture.argsCapture)).toString().split('\0').filter(Boolean)
        : JSON.parse((await readFile(fixture.argsCapture, 'utf8')).replace(/^\uFEFF/, ''));
      assert.deepEqual(args.filter((value) => value === '--file' || value === '-f').length, 1);
      assert.deepEqual(args.slice(-callerArgs.length), callerArgs);
      try {
        assert.doesNotMatch(await readFile(fixture.overrideCapture, 'utf8'), /Wechat_qc_code\.jpg/);
      } catch (error) {
        if (error.code !== 'ENOENT') throw error;
      }
      assert.equal(await readFile(fixture.composePath, 'utf8'), fixture.composeContents);
    });
  });

  test(`${spec.name} wrapper removes its override and preserves docker's exit code`, { skip: !spec.available }, async () => {
    await withFixture(spec, { dockerExit: 37 }, async (fixture) => {
      const result = spec.invoke(fixture.fixtureScript, ['up'], { cwd: fixture.fixtureRun, env: fixture.env });
      assert.equal(result.status, 37, result.stderr);
      const args = spec.name === 'POSIX shell'
        ? (await readFile(fixture.argsCapture)).toString().split('\0').filter(Boolean)
        : JSON.parse((await readFile(fixture.argsCapture, 'utf8')).replace(/^\uFEFF/, ''));
      const overrideIndex = args.findLastIndex((value, index) => index > 0 && (args[index - 1] === '--file' || args[index - 1] === '-f'));
      assert.notEqual(overrideIndex, -1);
      await assert.rejects(readFile(args[overrideIndex]), { code: 'ENOENT' });
    });
  });
}
