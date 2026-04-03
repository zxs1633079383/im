/**
 * API server address configuration.
 *
 * For local development: http://localhost:8080
 * For LAN usage: http://192.168.x.x:8080 (your server's LAN IP)
 *
 * Change this before building the Tauri app for distribution.
 */
export const API_HOST = 'http://196.168.1.99:8080';
export const API_BASE = `${API_HOST}/api`;
export const WS_BASE = API_HOST.replace(/^http/, 'ws') + '/ws';
