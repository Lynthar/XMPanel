import toast from "react-hot-toast";
import type { ServerInfo } from "./api";

/** The body of POST /servers/{id}/test. */
export interface TestResult {
  success: boolean;
  error?: string;
  info?: ServerInfo;
}

/**
 * Toasts a connection test: the failure with the backend's reason, or the
 * success with the detected auth mode and one lasting toast per probe warning.
 */
export function announceTestResult(
  result: TestResult,
  t: (key: string) => string,
): void {
  if (!result.success) {
    toast.error(`${t("servers.testFailed")}: ${result.error}`);
    return;
  }
  const mode = result.info?.auth_mode;
  toast.success(
    mode
      ? `${t("servers.testSuccess")} · ${t(`servers.authModes.${mode}`)}`
      : t("servers.testSuccess"),
  );
  for (const code of result.info?.warnings ?? []) {
    toast(t(`servers.warnings.${code}`), { icon: "⚠️", duration: 10000 });
  }
}
