import { Component, inject } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-home',
  standalone: true,
  imports: [RouterLink],
  template: `
    <div style="padding:2rem">
      <h1>Welcome, {{ auth.currentUser()?.display_name }}!</h1>
      <nav style="margin:1rem 0; display:flex; gap:1rem;">
        <a routerLink="/contacts">Contacts</a>
      </nav>
      <button (click)="logout()">Sign out</button>
    </div>
  `,
})
export class HomeComponent {
  auth = inject(AuthService);
  private router = inject(Router);

  logout(): void {
    this.auth.logout();
    this.router.navigate(['/login']);
  }
}
