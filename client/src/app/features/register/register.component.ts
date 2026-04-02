import { Component, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { FormsModule } from '@angular/forms';
import { CommonModule } from '@angular/common';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-register',
  standalone: true,
  imports: [FormsModule, CommonModule, RouterLink],
  templateUrl: './register.component.html',
  styleUrl: './register.component.scss',
})
export class RegisterComponent {
  username = '';
  email = '';
  password = '';
  displayName = '';
  loading = signal(false);
  errorMsg = signal('');

  constructor(
    private authService: AuthService,
    private router: Router,
  ) {}

  async onSubmit(): Promise<void> {
    if (!this.username || !this.email || !this.password) {
      this.errorMsg.set('Username, email, and password are required.');
      return;
    }
    this.loading.set(true);
    this.errorMsg.set('');
    try {
      await this.authService.register({
        username: this.username,
        email: this.email,
        password: this.password,
        display_name: this.displayName || this.username,
      });
      await this.router.navigate(['/']);
    } catch (err: any) {
      const msg = err?.error?.error ?? 'Registration failed. Please try again.';
      this.errorMsg.set(msg);
    } finally {
      this.loading.set(false);
    }
  }
}
