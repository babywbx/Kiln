const EMPTY_ERROR_STATE = Object.freeze({ clearPassword: false, invalidFields: [], focus: "" });

export function validateCredentials(username, password) {
  const usernameError = String(username || "").trim() ? "" : "login.error.usernameRequired";
  const passwordError = String(password || "") ? "" : "login.error.passwordRequired";
  if (!usernameError && !passwordError) return null;
  return {
    usernameError,
    passwordError,
    focus: usernameError ? "username" : "password",
  };
}

export function classifyLoginError(error) {
  const code = error?.code || "";
  const status = Number(error?.status || 0);
  if (code === "unauthorized" || status === 401) {
    return {
      key: "login.error.invalidCredentials",
      clearPassword: true,
      invalidFields: ["username", "password"],
      focus: "password",
    };
  }
  if (code === "too_many_requests" || status === 429) return { key: "login.error.rateLimited", ...EMPTY_ERROR_STATE };
  if (code === "invalid_request" || status === 400) return { key: "login.error.invalidRequest", ...EMPTY_ERROR_STATE };
  if (code === "forbidden" || status === 403) return { key: "login.error.accessDenied", ...EMPTY_ERROR_STATE };
  if (code === "unavailable" || code === "not_ready" || status === 502 || status === 503 || status === 504) {
    return { key: "login.error.serviceUnavailable", ...EMPTY_ERROR_STATE };
  }
  if (code === "internal" || status >= 500) return { key: "login.error.serverError", ...EMPTY_ERROR_STATE };
  if (error instanceof TypeError && status === 0) return { key: "login.error.networkError", ...EMPTY_ERROR_STATE };
  return { key: "login.error.unknown", ...EMPTY_ERROR_STATE };
}
