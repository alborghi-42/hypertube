import { isPlatformBrowser } from '@angular/common';
import { PLATFORM_ID, inject } from '@angular/core';
import { Router, type CanActivateFn } from '@angular/router';

import { AuthService } from './auth.service';

/**
 * Logged-in only routes. On the server (SSR/prerender) there is no session to check
 * (relative API URLs, no browser cookies), so it lets the render through and the
 * browser re-runs the guard on its initial navigation.
 */
export const authGuard: CanActivateFn = async (_route, state) => {
  if (!isPlatformBrowser(inject(PLATFORM_ID))) {
    return true;
  }
  const auth = inject(AuthService);
  const router = inject(Router);
  const user = await auth.loadUser();
  return user !== null ? true : router.createUrlTree(['/login'], { queryParams: { returnUrl: state.url } });
};

/** Login/register: send already logged-in users to the library. */
export const guestGuard: CanActivateFn = async () => {
  if (!isPlatformBrowser(inject(PLATFORM_ID))) {
    return true;
  }
  const auth = inject(AuthService);
  const router = inject(Router);
  const user = await auth.loadUser();
  return user === null ? true : router.createUrlTree(['/']);
};
