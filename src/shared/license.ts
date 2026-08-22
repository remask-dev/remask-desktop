import type { LicenseState } from "./api/types";

export const FREE_CUSTOM_RULE_LIMIT = 1;

export function hasProLicense(license: LicenseState) {
  const edition = license.edition?.trim().toLowerCase();
  return license.status === "valid" && Boolean(edition) && edition !== "free" && edition !== "trial";
}
