import { Routes } from '@angular/router';
import { authGuard } from './shared/guards/auth.guard';

export const routes: Routes = [
  {
    path: 'login',
    loadComponent: () =>
      import('./features/login/login.component').then((m) => m.LoginComponent),
  },
  {
    path: 'register',
    loadComponent: () =>
      import('./features/register/register.component').then((m) => m.RegisterComponent),
  },
  {
    path: '',
    canActivate: [authGuard],
    loadComponent: () =>
      import('./features/main-layout/main-layout.component').then(
        (m) => m.MainLayoutComponent
      ),
    children: [
      {
        path: '',
        loadComponent: () =>
          import('./features/home/home.component').then((m) => m.HomeComponent),
      },
      {
        path: 'contacts',
        loadComponent: () =>
          import('./features/contacts/contacts.component').then(
            (m) => m.ContactsComponent
          ),
      },
      {
        path: 'channels/:id',
        loadComponent: () =>
          import('./features/chat/chat.component').then((m) => m.ChatComponent),
      },
      {
        path: 'channels/:id/settings',
        loadComponent: () =>
          import('./features/channel-settings/channel-settings.component').then(
            (m) => m.ChannelSettingsComponent
          ),
      },
      {
        path: 'search',
        loadComponent: () =>
          import('./features/search/search.component').then(
            (m) => m.SearchComponent
          ),
      },
    ],
  },
  {
    path: '**',
    redirectTo: '',
  },
];
