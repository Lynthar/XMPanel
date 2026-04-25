import { create } from 'zustand'

interface User {
  id: number
  username: string
  email: string
  role: string
  mfa_enabled: boolean
}

// Auth store is in-memory only. The refresh token lives in an HttpOnly cookie
// (managed by the browser) and is invisible to JS, so we never persist tokens
// to localStorage. On page load the app calls /auth/refresh to mint a new
// access token from the cookie; if the cookie is missing or invalid the user
// is sent through /login.
interface AuthState {
  user: User | null
  accessToken: string | null
  isAuthenticated: boolean
  setAuth: (user: User, accessToken: string) => void
  setAccessToken: (accessToken: string) => void
  setUser: (user: User) => void
  logout: () => void
}

export const useAuthStore = create<AuthState>()((set) => ({
  user: null,
  accessToken: null,
  isAuthenticated: false,

  setAuth: (user, accessToken) =>
    set({
      user,
      accessToken,
      isAuthenticated: true,
    }),

  setAccessToken: (accessToken) => set({ accessToken }),

  setUser: (user) => set({ user }),

  logout: () =>
    set({
      user: null,
      accessToken: null,
      isAuthenticated: false,
    }),
}))
