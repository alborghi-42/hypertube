import { inject } from '@angular/core';
import { Router, Routes } from '@angular/router';

import { authGuard, guestGuard } from './auth/auth.guard';
import { ForgotPassword } from './auth/forgot-password/forgot-password';
import { Login } from './auth/login/login';
import { CompleteProfile } from './auth/oauth/complete-profile';
import { Register } from './auth/register/register';
import { ResetPassword } from './auth/reset-password/reset-password';
import { Browse } from './browse/browse';
import { DEFAULT_PROVIDER } from './media/media-api';
import { MoviePage } from './movie/movie-page';
import { Profile } from './profile/profile';
import { UserProfile } from './profile/user-profile';

/** `title` values are translation keys, see i18n/title-strategy.ts. */
export const routes: Routes = [
  // Library and video pages are for logged-in users only.
  { path: '', component: Browse, canActivate: [authGuard] },
  // Movie page: provider + external id resolve on the backend even before a media row exists.
  {
    path: 'movie/:provider/:externalId',
    component: MoviePage,
    title: 'titles.movie',
    canActivate: [authGuard],
  },
  // Old player-modal links (always archive.org ids at the time).
  {
    path: 'watch/:id',
    redirectTo: ({ params }) =>
      inject(Router).createUrlTree(['/movie', DEFAULT_PROVIDER, params['id'] ?? '']),
  },
  { path: 'profile', component: Profile, title: 'titles.profile', canActivate: [authGuard] },
  {
    path: 'users/:username',
    component: UserProfile,
    title: 'titles.userProfile',
    canActivate: [authGuard],
  },
  { path: 'login', component: Login, title: 'titles.login', canActivate: [guestGuard] },
  { path: 'register', component: Register, title: 'titles.register', canActivate: [guestGuard] },
  {
    path: 'oauth/complete',
    component: CompleteProfile,
    title: 'titles.completeProfile',
    canActivate: [guestGuard],
  },
  {
    path: 'forgot-password',
    component: ForgotPassword,
    title: 'titles.forgotPassword',
    canActivate: [guestGuard],
  },
  // No guard: the emailed link must also work in a browser that is logged in.
  { path: 'reset-password', component: ResetPassword, title: 'titles.resetPassword' },
  { path: '**', redirectTo: '' },
];
