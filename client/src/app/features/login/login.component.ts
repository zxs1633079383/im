import { Component, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { FormsModule } from '@angular/forms';
import { CommonModule } from '@angular/common';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-login',
  standalone: true,
  imports: [FormsModule, CommonModule, RouterLink],
  templateUrl: './login.component.html',
  styleUrl: './login.component.scss',
})
export class LoginComponent {
  login = '';
  password = '';
  loading = signal(false);
  errorMsg = signal('');

  constructor(
    private authService: AuthService,
    private router: Router,
  ) {}

  async onSubmit(): Promise<void> {
    if (!this.login || !this.password) {
      this.errorMsg.set('Please enter your username/email and password.');
      return;
    }
    this.loading.set(true);
    this.errorMsg.set('');
    try {
      await this.authService.login({ login: this.login, password: this.password });
      await this.router.navigate(['/']);
    } catch (err: any) {
      const msg = err?.error?.error ?? 'Login failed. Please try again.';
      this.errorMsg.set(msg);
    } finally {
      this.loading.set(false);
    }
  }
}
