import { HttpErrorResponse, type HttpInterceptorFn } from '@angular/common/http';
import { inject } from '@angular/core';
import { Router } from '@angular/router';
import { catchError, throwError } from 'rxjs';

import { AuthService } from './auth.service';

const API_URL = /^(\.?\/)?api\//;
const AUTH_URL = /^(\.?\/)?api\/auth\//;

/**
 * Sends the session cookie with every API call and handles expired sessions.
 * The CSRF header (X-XSRF-TOKEN from the XSRF-TOKEN cookie) is added by HttpClient's
 * built-in XSRF interceptor, configured in app.config.ts, for every mutating request.
 */
export const authInterceptor: HttpInterceptorFn = (req, next) => {
  if (!API_URL.test(req.url)) {
    return next(req);
  }
  const auth = inject(AuthService);
  const router = inject(Router);
  return next(req.clone({ withCredentials: true })).pipe(
    catchError((error: unknown) => {
      if (error instanceof HttpErrorResponse && error.status === 401 && !AUTH_URL.test(req.url)) {
        auth.markLoggedOut();
        void router.navigate(['/login'], { queryParams: { returnUrl: router.url } });
      }
      return throwError(() => error);
    }),
  );
};
