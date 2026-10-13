import { HttpErrorResponse } from '@angular/common/http';
import type { AbstractControl, FormGroup } from '@angular/forms';

import type { AuthErrorBody } from './auth.service';

/** First human-readable validation message for a control, or null. */
export function controlError(control: AbstractControl, label: string): string | null {
  const errors = control.errors;
  if (errors === null || !(control.touched || control.dirty)) {
    return null;
  }
  if (errors['server']) return String(errors['server']);
  if (errors['required']) return `${label} is required`;
  if (errors['email']) return 'Enter a valid email address';
  if (errors['minlength']) return `${label} must be at least ${errors['minlength'].requiredLength} characters`;
  if (errors['maxlength']) return `${label} must be at most ${errors['maxlength'].requiredLength} characters`;
  if (errors['pattern']) return `${label} has an invalid format`;
  return `${label} is invalid`;
}

/** Maps a backend error onto the form (per-field `fields`) and returns the top-level message. */
export function applyServerErrors(form: FormGroup, error: unknown, fallback: string): string {
  if (!(error instanceof HttpErrorResponse)) {
    return fallback;
  }
  if (error.status === 429) {
    return 'Too many failed attempts. Please wait a few minutes and try again.';
  }
  const body = (error.error ?? {}) as AuthErrorBody;
  for (const [field, message] of Object.entries(body.fields ?? {})) {
    const control = form.get(field);
    control?.setErrors({ ...(control.errors ?? {}), server: message });
    control?.markAsTouched();
  }
  return body.error ?? fallback;
}
