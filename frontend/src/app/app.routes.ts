import { Routes } from '@angular/router';

import { authGuard, guestGuard } from './auth/auth.guard';
import { ForgotPassword } from './auth/forgot-password/forgot-password';
import { Login } from './auth/login/login';
import { CompleteProfile } from './auth/oauth/complete-profile';
import { Register } from './auth/register/register';
import { ResetPassword } from './auth/reset-password/reset-password';
import { Browse } from './browse/browse';

export const routes: Routes = [
  // Library and video pages are for logged-in users only.
  { path: '', component: Browse, title: 'Hypertube', canActivate: [authGuard] },
  // Same page with the player modal open; deep-linkable.
  { path: 'watch/:id', component: Browse, title: 'Hypertube', canActivate: [authGuard] },
  { path: 'login', component: Login, title: 'Log in · Hypertube', canActivate: [guestGuard] },
  { path: 'register', component: Register, title: 'Sign up · Hypertube', canActivate: [guestGuard] },
  { path: 'oauth/complete', component: CompleteProfile, title: 'Complete your profile · Hypertube', canActivate: [guestGuard] },
  { path: 'forgot-password', component: ForgotPassword, title: 'Forgot password · Hypertube', canActivate: [guestGuard] },
  // No guard: the emailed link must also work in a browser that is logged in.
  { path: 'reset-password', component: ResetPassword, title: 'Reset password · Hypertube' },
  { path: '**', redirectTo: '' },
];
