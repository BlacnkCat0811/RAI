#include <stddef.h>
// Caller owns output. The gzip header has mtime=0; bytes are persisted once.
int rai_gzip(const unsigned char *input, size_t count, unsigned char **output, size_t *output_count);
void rai_free(void *buffer);
