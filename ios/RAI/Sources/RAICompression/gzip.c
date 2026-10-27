#include "RAICompression.h"
#include <stdlib.h>
#include <zlib.h>
#include <limits.h>

int rai_gzip(const unsigned char *input, size_t count, unsigned char **output, size_t *output_count) {
    if (count > UINT_MAX) return Z_BUF_ERROR;
    z_stream stream = {0};
    int result = deflateInit2(&stream, Z_DEFAULT_COMPRESSION, Z_DEFLATED, 15 + 16, 8, Z_DEFAULT_STRATEGY);
    if (result != Z_OK) return result;
    size_t capacity = deflateBound(&stream, count);
    unsigned char *buffer = malloc(capacity);
    if (!buffer) { deflateEnd(&stream); return Z_MEM_ERROR; }
    stream.next_in = (Bytef *)input; stream.avail_in = (uInt)count;
    stream.next_out = buffer; stream.avail_out = (uInt)capacity;
    result = deflate(&stream, Z_FINISH);
    if (result == Z_STREAM_END) { *output = buffer; *output_count = stream.total_out; }
    else free(buffer);
    deflateEnd(&stream);
    return result == Z_STREAM_END ? Z_OK : result;
}
void rai_free(void *buffer) { free(buffer); }
