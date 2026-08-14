// English is the source catalog and the runtime fallback for incomplete
// translations. Keep keys semantic and values as complete phrases so other
// languages can change word order. See ../README.md before adding messages.

export default Object.freeze({
  "common.actions.cancel": "Cancel",
  "common.actions.next": "Next",
  "common.actions.previous": "Previous",
  "common.labels.admin": "Admin",

  "navigation.overview": "Overview",
  "navigation.providers": "Providers",
  "navigation.models": "Models",
  "navigation.auditLogs": "Audit Logs",
  "navigation.usage": "Usage",
  "navigation.budgets": "Budgets",
  "navigation.rateLimits": "Rate Limits",
  "navigation.apiKeys": "API Keys",
  "navigation.workflows": "Workflows",
  "navigation.guardrailsExperimental": "Guardrails (experimental)",
  "navigation.mcpServers": "MCP Servers",
  "navigation.settings": "Settings",

  "sidebar.actions.signOut": "Sign out",
  "sidebar.actions.enterApiKey": "Enter API key",
  "sidebar.actions.changeApiKey": "Change API key",
  "sidebar.resize.label": "Resize sidebar",
  "sidebar.resize.help": "Drag to resize; click to collapse or expand",

  "theme.label": "Theme",
  "theme.light": "Light theme",
  "theme.system": "System theme",
  "theme.dark": "Dark theme",
  "theme.change": "Change theme (currently {theme})",

  "language.label": "Language",

  "auth.banner.required": "Authentication required for dashboard data.",
  "auth.dialog.lockedTitle": "Dashboard locked",
  "auth.dialog.changeKeyTitle": "Change API key",
  "auth.dialog.close": "Close authentication dialog",
  "auth.dialog.signInWithSso": "Sign in with SSO",
  "auth.dialog.orUseApiKey": "or use an API key",
  "auth.apiKey.label": "API key",
  "auth.apiKey.placeholder": "Master key or bearer token",
  "auth.apiKey.invalid": "Enter a valid API key to continue.",
  "auth.apiKey.storageHint":
    "Stored in this browser. Requests use the Authorization bearer header.",
  "auth.actions.unlockDashboard": "Unlock dashboard",
  "auth.actions.saveApiKey": "Save API key",

  "confirmation.close": "Close confirmation dialog",
  "notifications.dismiss": "Dismiss notification",

  "demoMode.label": "Demo mode notice",
  "demoMode.title": "Public demo",
  "demoMode.warning":
    "Do not enter sensitive or personal data. Demo data is reset regularly.",
  "demoMode.websiteLabel": "GoModel website",

  "pagination.summary": "Showing {start}–{end} of {total}",

  "datePicker.lastDays.one": "Last {count} day",
  "datePicker.lastDays.other": "Last {count} days",
  "datePicker.days.one": "{count} day",
  "datePicker.days.other": "{count} days",
  "datePicker.today": "Today",
  "datePicker.range": "{start} – {end}",
  "datePicker.openRange": "{start} – ...",
  "datePicker.selectStart": "Select start date",
  "datePicker.selectEnd": "Select end date",
  "datePicker.previousMonth": "Previous month",
  "datePicker.nextMonth": "Next month",
  "datePicker.weekdays.mondayShort": "Mo",
  "datePicker.weekdays.tuesdayShort": "Tu",
  "datePicker.weekdays.wednesdayShort": "We",
  "datePicker.weekdays.thursdayShort": "Th",
  "datePicker.weekdays.fridayShort": "Fr",
  "datePicker.weekdays.saturdayShort": "Sa",
  "datePicker.weekdays.sundayShort": "Su",

  "dateRange.chartTitle.daily": "Daily Token Usage",
  "dateRange.chartTitle.weekly": "Weekly Token Usage",
  "dateRange.chartTitle.monthly": "Monthly Token Usage",
  "dateRange.chartTitle.yearly": "Yearly Token Usage",
});
