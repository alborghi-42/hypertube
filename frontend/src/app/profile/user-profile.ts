import { HttpErrorResponse } from '@angular/common/http';
import { ChangeDetectionStrategy, Component, computed, inject, resource } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { TranslocoPipe } from '@jsverse/transloco';
import { map } from 'rxjs';

import { AuthService } from '../auth/auth.service';
import { ProfileApi } from './profile-api';

/** Public profile of any user (/users/:username). Never shows the email. */
@Component({
  selector: 'app-user-profile',
  imports: [RouterLink, TranslocoPipe],
  templateUrl: './user-profile.html',
  styleUrl: './profile.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class UserProfile {
  private readonly api = inject(ProfileApi);
  private readonly auth = inject(AuthService);

  /** The :username route param (a user id works too). */
  readonly username = toSignal(
    inject(ActivatedRoute).paramMap.pipe(map((params) => params.get('username') ?? '')),
    { initialValue: '' },
  );

  readonly profileResource = resource({
    params: () => (this.username() ? { key: this.username() } : undefined),
    loader: ({ params }) => this.api.get(params.key),
  });

  readonly profile = computed(() => this.profileResource.value() ?? null);
  readonly isSelf = computed(() => this.profile()?.id === this.auth.user()?.id);
  readonly notFound = computed(() => {
    const error = this.profileResource.error();
    return error instanceof HttpErrorResponse && error.status === 404;
  });
}
