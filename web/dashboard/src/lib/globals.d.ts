// Runtime globals the Go handler injects into index.html before the bundle
// loads. Ambient-only: keep this file free of imports/exports so the Window
// augmentation stays global.
interface Window {
  GOMODEL_BASE_PATH?: string;
  GOMODEL_VERSION?: string;
  GOMODEL_DEMO_MODE?: boolean;
}
