import { Routes } from '@angular/router';

import { authGuard, guestGuard } from './auth/auth.guard';
import { Login } from './auth/login/login';
import { Register } from './auth/register/register';
import { Browse } from './browse/browse';

export const routes: Routes = [
  // Library and video pages are for logged-in users only.
  { path: '', component: Browse, title: 'Hypertube', canActivate: [authGuard] },
  // Same page with the player modal open; deep-linkable.
  { path: 'watch/:id', component: Browse, title: 'Hypertube', canActivate: [authGuard] },
  { path: 'login', component: Login, title: 'Log in · Hypertube', canActivate: [guestGuard] },
  { path: 'register', component: Register, title: 'Sign up · Hypertube', canActivate: [guestGuard] },
  { path: '**', redirectTo: '' },
];
