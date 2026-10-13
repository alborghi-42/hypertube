import { Component } from '@angular/core';

@Component({
  selector: 'app-footer',
  template: `
    <footer class="app-footer">
      <span>Hypertube</span>
      <span>42 project · {{ year }}</span>
    </footer>
  `,
  styles: `
    :host {
      display: block;
      border-top: 1px solid var(--ht-border);
      background: var(--ht-bg-1);
    }

    .app-footer {
      margin: 0 auto;
      width: min(1400px, 100%);
      padding: 0.8rem var(--ht-space-sm);
      display: flex;
      flex-wrap: wrap;
      justify-content: space-between;
      gap: 0.4rem 1rem;
      color: var(--ht-text-2);
      font-family: var(--ht-font-mono);
      font-size: 0.8rem;
    }
  `,
})
export class AppFooter {
  protected readonly year = new Date().getFullYear();
}
