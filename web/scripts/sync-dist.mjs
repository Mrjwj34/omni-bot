import { cpSync, existsSync, mkdirSync, readdirSync, rmSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(scriptDir, '..');
const buildDir = path.join(webRoot, 'build');
const webuiDir = path.resolve(webRoot, '..', 'internal', 'webui');
const distDir = path.join(webuiDir, 'dist');
const preserved = new Set(['.gitkeep', 'README.md']);

if (!existsSync(buildDir)) {
	console.error(`sync-dist: build output not found at ${buildDir}`);
	process.exit(1);
}

// Keep Commit 1 decoupled from internal/webui until Go embed package exists.
if (!existsSync(webuiDir)) {
	process.exit(0);
}

mkdirSync(distDir, { recursive: true });

for (const entry of readdirSync(distDir)) {
	if (preserved.has(entry)) {
		continue;
	}
	rmSync(path.join(distDir, entry), { recursive: true, force: true });
}

for (const entry of readdirSync(buildDir)) {
	cpSync(path.join(buildDir, entry), path.join(distDir, entry), { recursive: true });
}
