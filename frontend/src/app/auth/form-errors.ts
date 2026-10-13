import { HttpErrorResponse } from '@angular/common/http';
import type { AbstractControl, FormGroup } from '@angular/forms';

import { msg, type Message } from '../i18n/message';
import type { AuthErrorBody } from './auth.service';

/**
 * First validation message for a control, or null. Messages do not repeat the field
 * name (it is the label right above), which keeps them grammatical in every language.
 * Render with the `message` pipe.
 */
export function controlError(control: AbstractControl): Message | null {
  const errors = control.errors;
  if (errors === null || !(control.touched || control.dirty)) {
    return null;
  }
  if (errors['server']) return { text: String(errors['server']) };
  if (errors['required']) return msg('validation.required');
  if (errors['email']) return msg('validation.email');
  if (errors['minlength'])
    return msg('validation.minlength', { length: errors['minlength'].requiredLength });
  if (errors['maxlength'])
    return msg('validation.maxlength', { length: errors['maxlength'].requiredLength });
  if (errors['pattern']) return msg('validation.pattern');
  return msg('validation.invalid');
}

/**
 * Maps a backend error onto the form (per-field `fields`) and returns the top-level message.
 * `fallbackKey` is a translation key; backend messages are shown as sent (English).
 */
export function applyServerErrors(form: FormGroup, error: unknown, fallbackKey: string): Message {
  if (!(error instanceof HttpErrorResponse)) {
    return msg(fallbackKey);
  }
  if (error.status === 429) {
    return msg('errors.tooManyAttempts');
  }
  const body = (error.error ?? {}) as AuthErrorBody;
  for (const [field, message] of Object.entries(body.fields ?? {})) {
    const control = form.get(field);
    control?.setErrors({ ...(control.errors ?? {}), server: message });
    control?.markAsTouched();
  }
  return body.error ? { text: body.error } : msg(fallbackKey);
}
