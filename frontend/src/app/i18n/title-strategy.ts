import { Injectable, effect, inject, signal } from '@angular/core';
import { Title } from '@angular/platform-browser';
import { type RouterStateSnapshot, TitleStrategy } from '@angular/router';

import { LanguageService } from '../shell/language.service';

const APP_NAME = 'Hypertube';

/**
 * Route `title`s are translation keys (no title = just the app name). The document title
 * is re-translated when the language changes.
 */
@Injectable({ providedIn: 'root' })
export class TranslatedTitleStrategy extends TitleStrategy {
  private readonly title = inject(Title);
  private readonly language = inject(LanguageService);
  private readonly key = signal<string | undefined>(undefined);

  constructor() {
    super();
    effect(() => this.apply(this.key()));
  }

  override updateTitle(snapshot: RouterStateSnapshot): void {
    const key = this.buildTitle(snapshot);
    this.key.set(key);
    this.apply(key); // synchronously too, so the server-rendered page has it
  }

  private apply(key: string | undefined): void {
    this.title.setTitle(key ? `${this.language.t(key)} · ${APP_NAME}` : APP_NAME);
  }
}
