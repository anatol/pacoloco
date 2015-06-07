#ifndef __BUFFER_H_
#define __BUFFER_H_


#define BUFFER_SIZE 4096

struct buffer {
  char data[BUFFER_SIZE];
  size_t inuse;
};

#define BUF_INIT()                                                                                                     \
  { .inuse = 0 };
static void buf_init(struct buffer *buf) { buf->inuse = 0; }
static void buf_reset(struct buffer *buf) { buf->inuse = 0; }

static void buf_shift(struct buffer *buf, size_t processed) {
  if (processed == buf->inuse) {
    // most likely incoming client buffer contains only 1 request and we completely processed it
    buf->inuse = 0;
  } else {
    assert(buf->inuse > processed);
    // but if the buffer contains multiple requests and we processed only part of it then we need to preserve the rest
    // of the buffer
    const char *rest = buf->data + processed;
    size_t rest_size = buf->inuse - processed;
    memmove(buf->data, rest, rest_size);
    buf->inuse = rest_size;
  }
}

static struct buffer *buf_clone(const struct buffer *src) {
  struct buffer *res = malloc(sizeof(struct buffer));
  *res = *src;
  return res;
}

static bool buf_full(struct buffer *buf) { return buf->inuse == BUFFER_SIZE; }

// read from file to the buffer
static ssize_t buf_read(int fd, struct buffer *buf) {
  assert(BUFFER_SIZE > buf->inuse);
  size_t avail = BUFFER_SIZE - buf->inuse;
  char *curr = buf->data + buf->inuse;
  ssize_t n;

restart:
  n = read(fd, curr, avail);
  if (n < 0 && errno == EINTR)
    goto restart;

  if (n > 0)
    buf->inuse += n;

  return n;
}

// write buffer *to* given file
static ssize_t buf_write(int fd, struct buffer *buf) {
  assert(buf->inuse > 0);
  ssize_t n;

restart:
  n = write(fd, buf->data, buf->inuse);
  if (n < 0) {
    if (errno == EINTR)
      goto restart;
  } else {
    assert((size_t)n == buf->inuse);
    buf->inuse = 0;
  }

  return n;
}

static int buf_printf(struct buffer *buf, const char *fmt, ...) {
  assert(BUFFER_SIZE > buf->inuse);
  size_t avail = BUFFER_SIZE - buf->inuse;
  char *curr = buf->data + buf->inuse;

  va_list args;
  va_start(args, fmt);
  int n = vsnprintf(curr, avail, fmt, args);
  va_end(args);
  if (n > 0)
    buf->inuse += n;

  return n;
}

static void buf_append(struct buffer *dest, const struct buffer *src) {
  assert(dest->inuse + src->inuse <= BUFFER_SIZE);
  memcpy(dest->data + dest->inuse, src->data, src->inuse);
  dest->inuse += src->inuse;
}


#endif /* __BUFFER_H_ */