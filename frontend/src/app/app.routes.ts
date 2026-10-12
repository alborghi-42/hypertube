import { Routes } from '@angular/router';

import { Browse } from './browse/browse';

export const routes: Routes = [
  { path: '', component: Browse, title: 'Hypertube' },
  // Same page with the player modal open; deep-linkable.
  { path: 'watch/:id', component: Browse, title: 'Hypertube' },
  { path: '**', redirectTo: '' },
];
