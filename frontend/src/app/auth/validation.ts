import { Validators, type ValidatorFn } from '@angular/forms';

/** Mirrors backend/internal/auth/validate.go. */
export const USERNAME_VALIDATORS: ValidatorFn[] = [
  Validators.required,
  Validators.minLength(3),
  Validators.maxLength(30),
  Validators.pattern(/^[A-Za-z0-9_.-]+$/),
];

export const EMAIL_VALIDATORS: ValidatorFn[] = [
  Validators.required,
  Validators.email,
  Validators.maxLength(254),
];

export const NAME_VALIDATORS: ValidatorFn[] = [Validators.required, Validators.maxLength(50)];

export const PASSWORD_VALIDATORS: ValidatorFn[] = [
  Validators.required,
  Validators.minLength(8),
  Validators.maxLength(128),
  Validators.pattern(/^(?=.*\p{L})(?=.*\p{N}).*$/u),
];
