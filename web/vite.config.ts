import tailwindcss from '@tailwindcss/vite';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, loadEnv } from 'vite';

export default defineConfig(({ mode }) => {
	const env = loadEnv(mode, '.', '');
	const kernelTarget = env.OMNI_KERNEL_URL || 'http://127.0.0.1:8080';

	return {
		plugins: [tailwindcss(), sveltekit()],
		server: {
			proxy: {
				'/ws': {
					target: kernelTarget,
					ws: true,
					changeOrigin: true
				},
				'/api': {
					target: kernelTarget,
					changeOrigin: true
				},
				'/media': {
					target: kernelTarget,
					changeOrigin: true
				}
			}
		}
	};
});
