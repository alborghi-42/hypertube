import {
  ApplicationConfig,
  inject,
  provideAppInitializer,
  provideBrowserGlobalErrorListeners,
} from '@angular/core';
import {
  provideHttpClient,
  withFetch,
  withInterceptors,
  withXsrfConfiguration,
} from '@angular/common/http';
import { TitleStrategy, provideRouter } from '@angular/router';

import { routes } from './app.routes';
import { provideClientHydration } from '@angular/platform-browser';
import { authInterceptor } from './auth/auth.interceptor';
import { TranslatedTitleStrategy } from './i18n/title-strategy';
import { provideI18n } from './i18n/transloco';
import { LanguageService } from './shell/language.service';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(routes),
    // No event replay: its inline script is blocked by the CSP (see backend/internal/httpsec).
    provideClientHydration(),
    provideHttpClient(
      withFetch(),
      withInterceptors([authInterceptor]),
      // Double-submit CSRF: the backend sets XSRF-TOKEN and checks X-XSRF-TOKEN on mutations.
      withXsrfConfiguration({ cookieName: 'XSRF-TOKEN', headerName: 'X-XSRF-TOKEN' }),
    ),
    provideI18n(),
    { provide: TitleStrategy, useExisting: TranslatedTitleStrategy },
    // Render the first view with its translations loaded (server and browser alike).
    provideAppInitializer(() => inject(LanguageService).init()),
  ],
};
