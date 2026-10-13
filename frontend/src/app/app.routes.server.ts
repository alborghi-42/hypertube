import { RenderMode, ServerRoute } from '@angular/ssr';

export const serverRoutes: ServerRoute[] = [
  // Rendered per request: pages depend on the session, the language and route params
  // (e.g. users/:username, movie/:provider/:externalId), so nothing is prerendered.
  { path: '**', renderMode: RenderMode.Server },
];
