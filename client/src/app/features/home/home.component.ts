import { Component, inject } from '@angular/core';
import { Router } from '@angular/router';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-home',
  standalone: true,
  template: `
    <div style="padding:2rem">
      <h1>Welcome, {{ auth.currentUser()?.display_name }}!</h1>
      <p>Home page — more features coming in Plan 3+.</p>
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
