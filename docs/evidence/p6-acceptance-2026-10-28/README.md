# P6 executable acceptance evidence

Synthetic datasets only. No production database, real HealthKit read, token or raw GPS is included.

Both simulator classes ran iOS 26.5 on an x86_64 host without a Metal device. The screenshots exercise the Material/Shape fallback; native system TabBar and scroll compositing are unreliable on this host. These files are limited layout regression baselines, not native GPU Liquid Glass approval. Physical iPhone and actual iOS18 runtime remain NOT VERIFIED.

Default runs: 8 native Normalizer tests plus navigation, full training command workflow, and scroll/memory tests per device. UI04 checks actual system accessibility environment, visible text clipping and detail/actions/weekly/activities/trends/feedback. Only infrastructure error -56 gets one retry. Real clipping findings are not suppressed. The iPhone17 final accessibility run paused only the owned simulator photo-analysis background process after repeated CPU-related audit timeouts; no performance claim uses that run.

CompareSnapshots.swift samples pixels from five unedited PNGs: Home, Training, SessionDetail, Analysis, Execution. Both independent launch comparisons passed with fixed limits (changed <= 10%, mean channel error <= 12). Swapping Home with Analysis failed the negative control. Golden files are never updated automatically.

Reproduce against a disposable actual Go API/Worker/PostgreSQL using scripts/p6-client-host.sh and scripts/test-p6-client.py. Build with local simulator signing (CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- DEVELOPMENT_TEAM=) and RAI_DEV_HTTP_HOST=127.0.0.1; the app still uses real Keychain. Run UI01/UI02/UI03 with default Dynamic Type and dark OS appearance; run UI04 separately with Accessibility XXXL, Light OS appearance and both Reduce Motion/Transparency enabled; restore defaults then run UI05 against the saved baseline. Tests require the private temporary configuration path and never embed its token.

Raw xcresult bundles and diagnostic logs remain under /tmp/rai-p6-*. Structured summaries here record actual results, including failed attempts. Do not interpret an unsigned Release build as a device install. P3/P6 real-device acceptance is DEFERRED / NOT VERIFIED; P7 and production deployment have not started.
