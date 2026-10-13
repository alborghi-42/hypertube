import { DOCUMENT, isPlatformBrowser } from '@angular/common';
import { Injectable, PLATFORM_ID, effect, inject, signal, untracked } from '@angular/core';
import { TranslocoService } from '@jsverse/transloco';
import { firstValueFrom } from 'rxjs';

import { AuthService } from '../auth/auth.service';
import { DEFAULT_LANGUAGE, isSupportedLanguage, type LanguageCode } from '../i18n/transloco';
import { ProfileApi } from '../profile/profile-api';

export interface Language {
  code: LanguageCode;
  label: string;
}

/** Endonyms, so not translated. Must match supportedLanguages in backend/internal/auth/profile.go. */
export const LANGUAGES: readonly Language[] = [
  { code: 'en', label: 'English' },
  { code: 'fr', label: 'Français' },
  { code: 'it', label: 'Italiano' },
];

const STORAGE_KEY = 'ht-language';

/**
 * UI language. Logged-out users keep their pick in localStorage; for a logged-in user
 * users.language wins (applied whenever the session user changes) and a pick is saved to it.
 * SSR always renders English: there is no localStorage or session on the server.
 */
@Injectable({ providedIn: 'root' })
export class LanguageService {
  private readonly isBrowser = isPlatformBrowser(inject(PLATFORM_ID));
  private readonly document = inject(DOCUMENT);
  private readonly transloco = inject(TranslocoService);
  private readonly auth = inject(AuthService);
  private readonly profiles = inject(ProfileApi);
  /** Only changes once the translations of the language are loaded. */
  private readonly state = signal<LanguageCode>(DEFAULT_LANGUAGE);
  private requested: LanguageCode = DEFAULT_LANGUAGE;

  readonly current = this.state.asReadonly();

  constructor() {
    effect(() => {
      const language = this.auth.user()?.language;
      if (isSupportedLanguage(language)) {
        untracked(() => void this.apply(language));
      }
    });
  }

  /** Loads the stored (or default) language; awaited by the app initializer. */
  init(): Promise<void> {
    return this.apply(this.readStored());
  }

  /** A pick from a language select: applies it and saves it for a logged-in user. */
  async choose(code: string): Promise<void> {
    if (!isSupportedLanguage(code)) {
      return;
    }
    await this.apply(code);
    const user = this.auth.user();
    if (user !== null && user.language !== code) {
      try {
        this.auth.adopt(await this.profiles.updateLanguage(code));
      } catch {
        // Not saved server-side; the choice still applies in this browser.
      }
    }
  }

  /** Translates in the active language; reading the signal keeps templates and computeds reactive. */
  t(key: string, params?: Record<string, unknown>): string {
    this.state();
    return this.transloco.translate(key, params);
  }

  private async apply(code: LanguageCode): Promise<void> {
    this.requested = code;
    this.store(code);
    await firstValueFrom(this.transloco.load(code));
    if (this.requested !== code) {
      return; // a newer pick won the race
    }
    this.transloco.setActiveLang(code);
    this.document.documentElement.lang = code;
    this.state.set(code);
  }

  private store(code: LanguageCode): void {
    if (!this.isBrowser) {
      return;
    }
    try {
      localStorage.setItem(STORAGE_KEY, code);
    } catch {
      // Storage can be unavailable (private mode, quota); the in-memory choice still applies.
    }
  }

  private readStored(): LanguageCode {
    if (!this.isBrowser) {
      return DEFAULT_LANGUAGE;
    }
    try {
      const stored = localStorage.getItem(STORAGE_KEY);
      return isSupportedLanguage(stored) ? stored : DEFAULT_LANGUAGE;
    } catch {
      return DEFAULT_LANGUAGE;
    }
  }
}
