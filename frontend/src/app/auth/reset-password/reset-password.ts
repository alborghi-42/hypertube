import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import {
  NonNullableFormBuilder,
  ReactiveFormsModule,
  type AbstractControl,
  type ValidationErrors,
} from '@angular/forms';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';

import { MessagePipe, msg, type Message } from '../../i18n/message';
import { applyServerErrors, controlError } from '../form-errors';
import { PasswordResetService } from '../password-reset.service';
import { PASSWORD_VALIDATORS } from '../validation';

/** Group validator: `confirm` must equal `password` (error set on `confirm`). */
function passwordsMatch(group: AbstractControl): ValidationErrors | null {
  const confirm = group.get('confirm');
  const mismatch = confirm !== null && confirm.value !== group.get('password')?.value;
  if (confirm !== null) {
    const { mismatch: _, ...rest } = confirm.errors ?? {};
    confirm.setErrors(
      mismatch ? { ...rest, mismatch: true } : Object.keys(rest).length ? rest : null,
    );
  }
  return mismatch ? { mismatch: true } : null;
}

@Component({
  selector: 'app-reset-password',
  imports: [ReactiveFormsModule, RouterLink, TranslocoPipe, MessagePipe],
  templateUrl: './reset-password.html',
  styleUrl: '../auth-form.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResetPassword {
  private readonly resets = inject(PasswordResetService);

  readonly token = inject(ActivatedRoute).snapshot.queryParamMap.get('token') ?? '';

  readonly form = inject(NonNullableFormBuilder).group(
    {
      password: ['', PASSWORD_VALIDATORS],
      confirm: [''],
    },
    { validators: passwordsMatch },
  );
  readonly submitting = signal(false);
  readonly done = signal(false);
  readonly formError = signal<Message | null>(null);

  confirmError(): Message | null {
    const control = this.form.controls.confirm;
    if (control.errors?.['mismatch'] && (control.touched || control.dirty)) {
      return msg('validation.mismatch');
    }
    return null;
  }

  readonly controlError = controlError;

  async submit(): Promise<void> {
    if (this.form.invalid || this.submitting()) {
      this.form.markAllAsTouched();
      return;
    }
    this.submitting.set(true);
    this.formError.set(null);
    try {
      await this.resets.reset(this.token, this.form.getRawValue().password);
      this.done.set(true);
    } catch (error) {
      this.formError.set(applyServerErrors(this.form, error, 'errors.resetFailed'));
    } finally {
      this.submitting.set(false);
    }
  }
}
