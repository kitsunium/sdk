// pico_guard - picohttpparser only takes its vectorised path when the compiler
// targets SSE4.2. Without it the comparison silently measures the scalar
// fallback, so the build stops instead.
#ifndef __SSE4_2__
#error "picohttpparser must be built with SSE4.2 (-march=native on an SSE4.2 host)"
#endif

extern const int cref_pico_sse42;
const int cref_pico_sse42 = 1;
