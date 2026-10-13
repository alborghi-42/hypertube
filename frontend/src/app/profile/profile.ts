import { HttpErrorResponse } from '@angular/common/http';
import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  computed,
  effect,
  inject,
  signal,
} from '@angular/core';
import { NonNullableFormBuilder, ReactiveFormsModule, Validators } from '@angular/forms';
import { RouterLink } from '@angular/router';

import { AuthService, type AuthErrorBody } from '../auth/auth.service';
import { applyServerErrors, controlError } from '../auth/form-errors';
import { EMAIL_VALIDATORS, NAME_VALIDATORS, USERNAME_VALIDATORS } from '../auth/validation';
import { AVATAR_MAX_BYTES, AVATAR_TYPES, LANGUAGES, ProfileApi } from './profile-api';

/** Own profile: edit the account fields and upload a new avatar. */
@Component({
  selector: 'app-profile',
  imports: [ReactiveFormsModule, RouterLink],
  templateUrl: './profile.html',
  styleUrl: './profile.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class Profile {
  private readonly auth = inject(AuthService);
  private readonly api = inject(ProfileApi);

  readonly languages = LANGUAGES;
  readonly acceptTypes = AVATAR_TYPES.join(',');
  readonly user = this.auth.user;

  readonly form = inject(NonNullableFormBuilder).group({
    email: ['', EMAIL_VALIDATORS],
    username: ['', USERNAME_VALIDATORS],
    first_name: ['', NAME_VALIDATORS],
    last_name: ['', NAME_VALIDATORS],
    language: ['en', Validators.required],
  });
  readonly saving = signal(false);
  readonly formError = signal<string | null>(null);
  readonly saved = signal(false);

  readonly avatarFile = signal<File | null>(null);
  readonly previewUrl = signal<string | null>(null);
  readonly uploading = signal(false);
  readonly avatarError = signal<string | null>(null);
  readonly shownAvatar = computed(() => this.previewUrl() ?? this.user()?.avatar_url ?? null);

  readonly controlError = controlError;

  constructor() {
    // Fill the form once the user is known (the guard already loaded it in the browser).
    let filled = false;
    effect(() => {
      const user = this.user();
      if (user !== null && !filled) {
        filled = true;
        this.resetForm();
      }
    });
    inject(DestroyRef).onDestroy(() => this.setPreview(null));
  }

  resetForm(): void {
    const user = this.user();
    if (user === null) return;
    this.form.reset({
      email: user.email,
      username: user.username,
      first_name: user.first_name,
      last_name: user.last_name,
      language: user.language,
    });
    this.formError.set(null);
  }

  async save(): Promise<void> {
    if (this.form.invalid || this.saving()) {
      this.form.markAllAsTouched();
      return;
    }
    this.saving.set(true);
    this.saved.set(false);
    this.formError.set(null);
    const value = this.form.getRawValue();
    try {
      const user = await this.api.update({
        email: value.email.trim(),
        username: value.username.trim(),
        first_name: value.first_name.trim(),
        last_name: value.last_name.trim(),
        language: value.language,
      });
      this.auth.adopt(user);
      this.resetForm();
      this.saved.set(true);
    } catch (error) {
      this.formError.set(
        applyServerErrors(this.form, error, 'Could not save your profile, please try again'),
      );
    } finally {
      this.saving.set(false);
    }
  }

  onFileSelected(event: Event): void {
    const input = event.target as HTMLInputElement;
    const file = input.files?.[0] ?? null;
    input.value = '';
    this.avatarError.set(null);
    if (file === null) return;
    if (!AVATAR_TYPES.includes(file.type)) {
      this.avatarError.set('Choose a JPEG, PNG, WebP or GIF image');
      return;
    }
    if (file.size > AVATAR_MAX_BYTES) {
      this.avatarError.set('Image must be at most 5 MB');
      return;
    }
    this.avatarFile.set(file);
    this.setPreview(URL.createObjectURL(file));
  }

  cancelAvatar(): void {
    this.avatarFile.set(null);
    this.avatarError.set(null);
    this.setPreview(null);
  }

  async uploadAvatar(): Promise<void> {
    const file = this.avatarFile();
    if (file === null || this.uploading()) return;
    this.uploading.set(true);
    this.avatarError.set(null);
    try {
      this.auth.adopt(await this.api.uploadAvatar(file));
      this.cancelAvatar();
    } catch (error) {
      const body = error instanceof HttpErrorResponse ? ((error.error ?? {}) as AuthErrorBody) : {};
      this.avatarError.set(body.error ?? 'Upload failed, please try again');
    } finally {
      this.uploading.set(false);
    }
  }

  private setPreview(url: string | null): void {
    const old = this.previewUrl();
    if (old !== null) URL.revokeObjectURL(old);
    this.previewUrl.set(url);
  }
}
