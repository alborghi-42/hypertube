import { Injectable, isDevMode, type EnvironmentProviders, type Provider } from '@angular/core';
import { provideTransloco, type Translation, type TranslocoLoader } from '@jsverse/transloco';
import { from, type Observable } from 'rxjs';

export const SUPPORTED_LANGUAGES = ['en', 'fr', 'it'] as const;
export type LanguageCode = (typeof SUPPORTED_LANGUAGES)[number];
export const DEFAULT_LANGUAGE: LanguageCode = 'en';

/**
 * Translations are bundled with dynamic import() (one lazy chunk per language) instead of
 * fetched over HTTP: relative HttpClient URLs have no origin during SSR, while an import
 * works the same on the server and in the browser.
 */
const TRANSLATIONS: Record<LanguageCode, () => Promise<{ default: Translation }>> = {
  en: () => import('./translations/en.json'),
  fr: () => import('./translations/fr.json'),
  it: () => import('./translations/it.json'),
};

export function isSupportedLanguage(code: unknown): code is LanguageCode {
  return typeof code === 'string' && (SUPPORTED_LANGUAGES as readonly string[]).includes(code);
}

@Injectable({ providedIn: 'root' })
export class BundledTranslationLoader implements TranslocoLoader {
  getTranslation(lang: string): Observable<Translation> {
    const load = TRANSLATIONS[isSupportedLanguage(lang) ? lang : DEFAULT_LANGUAGE];
    return from(load().then((module) => module.default));
  }
}

export function provideI18n(): (Provider | EnvironmentProviders)[] {
  return provideTransloco({
    config: {
      availableLangs: [...SUPPORTED_LANGUAGES],
      defaultLang: DEFAULT_LANGUAGE,
      fallbackLang: DEFAULT_LANGUAGE,
      reRenderOnLangChange: true,
      missingHandler: { useFallbackTranslation: true, logMissingKey: isDevMode() },
      prodMode: !isDevMode(),
    },
    loader: BundledTranslationLoader,
  });
}
