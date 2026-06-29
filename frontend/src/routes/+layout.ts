// SPA mode: the Go server owns routing fallback, so render entirely on the
// client. No prerendering of dynamic dashboard routes.
export const ssr = false;
export const prerender = false;
