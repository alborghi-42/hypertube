import { Component } from '@angular/core';
import { RouterOutlet } from '@angular/router';

import { AppFooter } from './shell/app-footer';
import { AppHeader } from './shell/app-header';

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, AppHeader, AppFooter],
  templateUrl: './app.html',
  styleUrl: './app.scss',
})
export class App {}
