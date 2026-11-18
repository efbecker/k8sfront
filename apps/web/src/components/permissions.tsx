import { createContext, useContext, type ReactNode } from "react";
import { roleRank, type Role } from "@/lib/api";

// The role of the signed-in user. Outside the app shell (login page) nothing
// is restricted, so the default is admin. The API enforces every rule anyway;
// this only keeps the interface honest about what the user can do.
const RoleContext = createContext<Role>("admin");

export function RoleProvider({ role, children }: { role: Role; children: ReactNode }) {
  return <RoleContext.Provider value={role}>{children}</RoleContext.Provider>;
}

export function useRole(): Role {
  return useContext(RoleContext);
}

/** True when the current user has at least `min` (always true without `min`). */
export function useCan(min?: Role): boolean {
  const role = useRole();
  return !min || roleRank[role] >= roleRank[min];
}

/** Renders children only for users with at least `min`. */
export function Can({ min, children, fallback = null }: { min: Role; children: ReactNode; fallback?: ReactNode }) {
  return useCan(min) ? <>{children}</> : <>{fallback}</>;
}
