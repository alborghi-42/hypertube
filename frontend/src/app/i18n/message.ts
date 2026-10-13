import { Pipe, inject, type PipeTransform } from '@angular/core';

import { LanguageService } from '../shell/language.service';

/** Interpolation values of a translation ({{name}} placeholders). */
export type MessageParams = Record<string, unknown>;

/**
 * A message produced in TypeScript (form errors, failures): either a translation key, or
 * text shown as-is (backend error messages, which stay in English).
 */
export type Message = { key: string; params?: MessageParams } | { text: string };

export function msg(key: string, params?: MessageParams): Message {
  return { key, params };
}

/**
 * Renders a Message in the active language. It reads the LanguageService signal, so even
 * OnPush views re-render on a language switch.
 */
@Pipe({ name: 'message', pure: false })
export class MessagePipe implements PipeTransform {
  private readonly language = inject(LanguageService);

  transform(message: Message | null | undefined): string {
    if (!message) {
      return '';
    }
    return 'text' in message ? message.text : this.language.t(message.key, message.params);
  }
}
