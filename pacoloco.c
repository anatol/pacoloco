#define _BSD_SOURCE
#define _XOPEN_SOURCE
#define _DEFAULT_SOURCE
#define _POSIX_C_SOURCE 200809L

#include <arpa/inet.h>
#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <sys/uio.h>
#include <time.h>
#include <unistd.h>

#include "buffer.h"
#include "compat.h"
#include "ini.h"
#include "list.h"
#include "picohttpparser.h"
#include "uriparser.h"
#include "util.h"

// TODO: Add ipv6 support

#define SOCKET_BACKLOG 10
#define EPOLL_MAX_EVENTS 10
#define HTTP_HEADERS_MAX 30
#define MAX_URL_LEN 4096

#define DEFAULT_UPSTREAM "http://mirrors.kernel.org/archlinux"
#define DEFAULT_PORT 9129
/* define the config struct type */
struct config {
  char *upstream;
  int port;
};
static struct config config = {.upstream = DEFAULT_UPSTREAM, .port = DEFAULT_PORT};

static int epollfd;

typedef void(handler_t)(uint32_t events, void *data);

// TODO: save stats to /var dir
struct statistics {
  long failed_upstream; // upstream server does not respond
  long served_upstream;
  long served_locally;
  long not_modified; // db files are the same as upstream, so skip redirecting it
  // the sum of requests above can be bigger than number of served requests,
  // e.g. db check request might fail but we still redirect it upstream hoping that it is some kind of transient error.
  long unknown_repo_requests; // neither db nor package
  long served_total;
};
static struct statistics statistics;

enum peer_state { NEW, CONNECTING, ACTIVE, FAILED };

struct peer {
  int fd; // 0 means connection closed and we need to open it before using
  char *host;
  struct sockaddr_storage address; // numeric representation of host

  int port;
  char *pkg_prefix;
  char *db_prefix;

  struct list_head list;
  enum peer_state state;

  unsigned long shared;
  unsigned long received;
  // add architecture?

  struct list_head reqs_head; // list of peer_req going to this client

  handler_t *on_event; // epoll handler embedded directly to this structure

  // data buffer for the peer
  // in CONNECTING state this buffer contains output data
  // in ACTIVE state input data that was partially read from the peer
  struct buffer *buffer;
};
static LIST_HEAD(peers_head);
static struct peer upstream; // upstream looks a lot like peer repo

struct peer_req {
  struct peer *peer;
  struct list_head peer_req; // peer->reqs_head
  struct incoming_req *incoming_req;
  struct list_head file_check_req; // file_check->reqs_head
};

struct file_check {
  struct list_head reqs_head; // list of peer_req
  bool db;
  char *filename;
  struct peer *orig_peer; // peer at the host where client came from

  time_t if_modified_since; // value of "If-Modified-Since" header. Se only for db.
  time_t best_peer_time;
  time_t upstream_time;
  struct peer *best_peer;
};

struct incoming_req {
  struct client *client;
  struct list_head pipeline; // incoming_client->pipeline_requests
  struct buffer *output;     // the pipeline request has been processed and here is output already
  struct file_check *file_check;
};

struct client {
  int fd;
  // if not NULL then it should not be empty (i.e. contain data from previous reads)
  struct buffer *input;
  // to support pipeline we need to keep ordered list of file_check structures
  struct list_head pipeline_head; // list of incoming_req
  handler_t *on_event;
};

static void file_check_free(struct file_check *file_check) {
  struct peer_req *r, *t;
  list_for_each_safe(r, t, &file_check->reqs_head, file_check_req) {
    list_del(&r->file_check_req);
    // setting incoming_req to NULL means that incoming request has gone
    // peer hanler is responsible for freeing these objects
    r->incoming_req = NULL;
  }
  free(file_check->filename);
  free(file_check);
}

static void incoming_req_free(struct incoming_req *req) {
  // if request already has output then it means it is already processed and there should no be any file_check->reqs
  assert((req->output == NULL) ^ (req->file_check == NULL));

  list_del(&req->pipeline);

  if (req->output)
    free(req->output);

  if (req->file_check)
    file_check_free(req->file_check);

  free(req);
}

static void incoming_client_free(struct client *client) {
  struct incoming_req *r, *t;
  list_for_each_safe(r, t, &client->pipeline_head, pipeline) { incoming_req_free(r); }

  debug("[%d] closing client socket", client->fd);
  shutdown(client->fd, SHUT_RDWR);
  close(client->fd);
  free(client->input);
  free(client);
}

static char *readable_size(unsigned long input_size, char *buf) {
  double size = input_size;
  int i = 0;
  static const char *units[] = {"B", "kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"};
  while (size > 1000) {
    size /= 1000;
    i++;
  }
  sprintf(buf, "%.*f %s", i, size, units[i]);
  return buf;
}

static void format_url(char buff[MAX_URL_LEN], struct peer *peer, bool db, const char *filename) {
  const char *prot = (peer->port == 443) ? "https" : "http";
  char *prefix = db ? peer->db_prefix : peer->pkg_prefix;
  snprintf(buff, MAX_URL_LEN, "%s://%s:%d/%s/%s", prot, peer->host, peer->port, prefix, filename);
}

#define RPC_PREFIX "/rpc/"
#define REPO_PREFIX "/repo/"

// finds all completed incoming requests at the beginning of pipeline and writes then to socket
static void client_pipeline_flush(struct client *client) {
  struct incoming_req *r, *t;
  list_for_each_safe(r, t, &client->pipeline_head, pipeline) {
    struct buffer *buf = r->output;
    if (!buf)
      break;

    buf_write(client->fd, buf);
    incoming_req_free(r);
  }
}

// write with support of HTTP pipelining
static void client_write(struct client *client, struct incoming_req *req, struct buffer *output) {
  // to support HTTP pipeline we need to check whether pipeline is blocked
  if (list_empty(&client->pipeline_head)) {
    buf_write(client->fd, output);
    if (req)
      incoming_req_free(req);
  } else if (req == list_first_entry(&client->pipeline_head, struct incoming_req, pipeline)) {
    // processed request first in pipeline, thus we can write current data directly to socket
    buf_write(client->fd, output);
    incoming_req_free(req);
    client_pipeline_flush(client);
  } else {
    // pipeline is blocked
    if (!req) {
      req = malloc(sizeof(struct incoming_req));
      memset(req, 0, sizeof(struct incoming_req));
      req->client = client;
      list_add_tail(&client->pipeline_head, &req->pipeline);
    } else {
      file_check_free(req->file_check);
      req->file_check = NULL;
    }

    req->output = buf_clone(output);
  }
}

static void incoming_req_send_reply(struct incoming_req *req, int code, const char *msg) {
  int fd = req->client->fd;
  struct buffer output = BUF_INIT();
  buf_printf(&output, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", code, msg);
  client_write(req->client, req, &output);
  debug("[%d] send reply code=%d", fd, code);
}

static void client_send_reply(struct client *client, int code, const char *msg) {
  struct buffer output = BUF_INIT();
  buf_printf(&output, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", code, msg);
  client_write(client, NULL, &output);
  debug("[%d] send reply code=%d", client->fd, code);
}

// returns last part of the filename path
static const char *flatname(const char *filename) {
  const char *result = strrchr(filename, '/');
  return result ? result + 1 : filename;
}

static bool is_upstream(struct peer *peer) { return (peer == &upstream); }

static void incoming_req_redirect_to(struct incoming_req *req, struct peer *peer) {
  struct file_check *file_check = req->file_check;
  struct client *client = req->client;
  const char *filename = file_check->filename;
  if (!is_upstream(peer)) {
    // local repos have flat structure
    filename = flatname(filename);
  }

  char url[MAX_URL_LEN];
  format_url(url, peer, file_check->db, filename);

  struct buffer output = BUF_INIT();
  buf_printf(&output, "HTTP/1.1 307 Temporary Redirect\r\nLocation: %s\r\nContent-Length: 0\r\n\r\n", url);
  client_write(client, req, &output);
  debug("[%d] send redirect to url %s", client->fd, url);
}

static void client_send_ok_reply(struct client *client, const char *content_type, struct buffer *body) {
  struct buffer output = BUF_INIT();
  size_t body_size = body ? body->inuse : 0;
  buf_printf(&output, "HTTP/1.1 200 OK\r\nContent-Length: %zu\r\nContent-Type: %s\r\n\r\n", body_size, content_type);
  if (body)
    buf_append(&output, body);
  client_write(client, NULL, &output);
}

static void handle_peer_list(struct client *client) {
  struct peer *p;
  unsigned long total_saved = 0;

  struct buffer output = BUF_INIT();
  buf_printf(&output, "<html><head><title>Pacoloco hub status</title></head><body>"
                      "<h1>Available peers</h1><ul>");
  list_for_each(p, &peers_head, list) {
    const char *color = (p->state == FAILED) ? "grey" : "green";

    char shared_buf[10], received_buf[10];
    buf_printf(&output, "<li><span style='color:%s'>%s</span> (shared: %s, received: %s)", color, p->host,
               readable_size(p->shared, shared_buf),
               readable_size(p->received, received_buf)); // hostname instead of ip?
    if (p->pkg_prefix) {
      char url[MAX_URL_LEN];
      format_url(url, p, false, "");
      buf_printf(&output, " <a href='%s'>packages</a>", url);
    }
    if (p->db_prefix) {
      char url[MAX_URL_LEN];
      format_url(url, p, true, "");
      buf_printf(&output, " <a href='%s'>database</a>", url);
    }

    buf_printf(&output, "</li>");
    total_saved += p->shared;
  }

  char saved_buf[10];
  buf_printf(&output, "</ul><h4>Total saved: %s</h4>", readable_size(total_saved, saved_buf));
  buf_printf(&output, "<h4>Request statistics:</h4><ul>");

  buf_printf(&output, "<li>served total: %ld</li>", statistics.served_total);
  buf_printf(&output, "<li>served upstream: %ld</li>", statistics.served_upstream);
  buf_printf(&output, "<li>served locally: %ld</li>", statistics.served_locally);
  buf_printf(&output, "<li>database not modified: %ld</li>", statistics.not_modified);
  buf_printf(&output, "<li>upstream server did not reply: %ld</li>", statistics.failed_upstream);
  buf_printf(&output, "<li>unknown repo requests: %ld</li>", statistics.unknown_repo_requests);

  buf_printf(&output, "</ul></body></html>\n");

  client_send_ok_reply(client, "text/html", &output);
}

static void peer_close(struct peer *peer) {
  close(peer->fd);
  peer->fd = 0;
  peer->state = NEW;
  buf_reset(peer->buffer);

  // cancel all requests sent to the peer
  struct peer_req *req, *t;
  list_for_each_safe(req, t, &peer->reqs_head, peer_req) {
    list_del(&req->peer_req);
    struct incoming_req *incoming_req = req->incoming_req;
    if (incoming_req)
      list_del(&req->file_check_req);
    free(req);
    if (!incoming_req)
      continue; // peer_req is handled already

    struct client *client = incoming_req->client;
    struct file_check *file_check = incoming_req->file_check;
    if (list_empty(&file_check->reqs_head)) {
      // it was the last peer request, no luck, send redirect upstream
      debug("[%d] no suitable repo found", client->fd);
      incoming_req_redirect_to(incoming_req, &upstream);
      statistics.served_upstream++;
    }
  }
}

static void peer_mark_inactive(struct peer *peer) {
  if (peer->fd) {
    peer_close(peer);
  }
  peer->state = FAILED;
}

#define HTTP_DATE_FMT "%a, %d %b %Y %H:%M:%S GMT"

static time_t parse_http_date(const char *value) {
  if (!value)
    return 0;

  struct tm tm;
  const char *res = strptime(value, HTTP_DATE_FMT, &tm);
  if (*res != '\0') {
    debug("incorrect date header format: %s", value);
    return 0;
  }
  time_t result = timegm(&tm);
  return result;
}

static time_t header_as_date(struct phr_header *headers, size_t headers_num, const char *header_name) {
  for (size_t i = 0; i < headers_num; i++) {
    if (strncmp(headers[i].name, header_name, headers[i].name_len) == 0) {
      char buff[120];
      size_t len = min(headers[i].value_len, (size_t)(120 - 1)); // 1 byte of the buffer for NULL
      strncpy(buff, headers[i].value, len);
      buff[len] = '\0';
      return parse_http_date(buff);
    }
  }

  return 0;
}

static void peer_calculate_stats(struct peer *dest, struct peer *src, struct phr_header *headers, size_t headers_num) {
  long file_size = 0;
  for (size_t i = 0; i < headers_num; i++) {
    if (strncmp(headers[i].name, "Content-Length", headers[i].name_len) == 0) {
      char buff[120];
      size_t len = min(headers[i].value_len, (size_t)(120 - 1)); // 1 byte of the buffer for NULL
      strncpy(buff, headers[i].value, len);
      buff[len] = '\0';
      file_size = atol(buff);
    }
  }

  assert(file_size > 0);
  src->shared += file_size;
  if (dest)
    dest->received += file_size;
}

static void handle_peer_response(struct peer *peer, int status, struct phr_header *headers, size_t headers_num) {
  assert(!list_empty(&peer->reqs_head));

  struct peer_req *req = list_first_entry(&peer->reqs_head, struct peer_req, peer_req);
  debug("[%d] got reply %d for client %d", peer->fd, status, req->incoming_req->client->fd);
  list_del(&req->peer_req);
  struct incoming_req *incoming_req = req->incoming_req;
  if (incoming_req)
    list_del(&req->file_check_req);
  free(req);

  if (!incoming_req) {
    debug("[%d] no incoming request, it must be handled/cancelled already", peer->fd);
    return;
  }

  struct client *client = incoming_req->client;
  struct file_check *file_check = incoming_req->file_check;
  bool peer_is_upstream = is_upstream(peer);

  if (status == 200) {
    // file exists at the server
    if (file_check->db) {
      time_t modified = header_as_date(headers, headers_num, "Last-Modified");
      debug("[%d] modified date %ld", peer->fd, modified);
      if (peer_is_upstream) {
        // got reply from upstream
        file_check->upstream_time = modified;
        // if upstream time is the same as If-Modified-Since then return 'Not-Modified'
        time_t modified_since = file_check->if_modified_since;
        if (modified_since && modified_since >= modified) {
          incoming_req_send_reply(incoming_req, 304, "Not Modified");
          statistics.not_modified++;
          return;
        }

        time_t best_peer_time = file_check->best_peer_time;
        if (best_peer_time && best_peer_time >= modified) {
          struct peer *dest_peer = file_check->orig_peer;
          struct peer *src_peer = file_check->best_peer;
          incoming_req_redirect_to(incoming_req, file_check->best_peer);
          statistics.served_locally++;
          peer_calculate_stats(dest_peer, src_peer, headers, headers_num);
          return;
        }
      } else {
        if (!file_check->best_peer || file_check->best_peer_time < modified) {
          // best peer is the one that has the latest database file
          file_check->best_peer = peer;
          file_check->best_peer_time = modified;

          time_t upstream_time = file_check->upstream_time;
          if (upstream_time && modified >= upstream_time) {
            struct peer *dest_peer = file_check->orig_peer;
            incoming_req_redirect_to(incoming_req, peer);
            statistics.served_locally++;
            peer_calculate_stats(dest_peer, peer, headers, headers_num);
            return;
          }
        }
      }
    } else {
      struct peer *dest_peer = file_check->orig_peer;
      // if it is a package file request then existence all we need
      incoming_req_redirect_to(incoming_req, peer);
      statistics.served_locally++;
      peer_calculate_stats(dest_peer, peer, headers, headers_num);

      return;
    }
  } else if (status == 404) {
    if (peer_is_upstream) {
      log_warn("[%d] requested database file does not exist upstream", peer->fd);
      incoming_req_send_reply(incoming_req, 404, "Not Found");
      statistics.failed_upstream++;
      return;
    }
  } else {
    log_info("[%d] unexpected response code: %d", peer->fd, status);
  }

  if (list_empty(&file_check->reqs_head)) {
    // it was the last peer request, no luck, send redirect upstream
    debug("[%d] no suitable repo found", client->fd);
    incoming_req_redirect_to(incoming_req, &upstream);
    statistics.served_upstream++;
  }
}

static void peer_event_handler(uint32_t events, void *data) {
  struct peer *peer = container_of(data, struct peer, on_event);
  int fd = peer->fd;

  if (events & EPOLLOUT) {
    assert(peer->state == CONNECTING);

    if (events & EPOLLERR) {
      log_err("[%d] connection error", fd);
      peer_mark_inactive(peer);
      return;
    }

    struct epoll_event ev;
    ev.events = EPOLLIN | EPOLLRDHUP;
    ev.data.ptr = &peer->on_event;
    if (epoll_ctl(epollfd, EPOLL_CTL_MOD, fd, &ev) < 0) {
      perror("epoll_ctl");
      return;
    }

    int error = 0;
    socklen_t len = sizeof(error);
    if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &len) < 0) {
      perror("getsockopt");
      return;
    }

    if (error > 0) {
      log_err("[%d] connection error", fd);
      peer_mark_inactive(peer);
      return;
    }

    peer->state = ACTIVE;
    if (peer->buffer->inuse)
      buf_write(peer->fd, peer->buffer);
    // now peer->buffer becomes input buffer
    debug("[%d] opened a connection to peer %s", fd, peer->host);
  }

  if (events & EPOLLHUP || events & EPOLLERR || events & EPOLLRDHUP) {
    debug("[%d] got HUP for peer connection", fd);
    peer_close(peer);
    return;
  }

  if (events & EPOLLIN) {
    size_t bytes_left = 0, bytes_left_last = 0;
    while (true) {
      // TODO: we can make input stack allocated, and only in case of partial parsing allocate on heap
      struct buffer *buf = peer->buffer;
      char *buf_start = &buf->data[0];

      int n = buf_read(fd, buf);
      if (n < 0) {
        perror("buf_read");
        peer_close(peer);
        return;
      } else if (n == 0) {
        break;
      }
      bool is_full = buf_full(buf);
      bytes_left_last = bytes_left;
      bytes_left = buf->inuse;

      while (bytes_left > 0) {
        // incoming data might contain multiple HTTP requests (aka HTTP pipeline)
        int minor_version, status;
        const char *msg;
        struct phr_header headers[HTTP_HEADERS_MAX];
        size_t msg_len, headers_num = HTTP_HEADERS_MAX;

        int parsed = phr_parse_response(buf_start, bytes_left, &minor_version, &status, &msg, &msg_len, headers,
                                        &headers_num, bytes_left_last);

        if (parsed == -2) {
          if (is_full) {
            // input buffer is full but still does not contain full request
            // either income buffer is too small or user request is too long
            log_info("[%d] request is too long", fd);
            peer_close(peer);
            return;
          }
          // request is incomplete, continue the loop
          break;
        } else if (parsed == -1) {
          log_info("[%d] HTTP request parse error", fd);
          peer_close(peer);
          return;
        }
        assert(parsed > 0 && (size_t)parsed <= bytes_left);

        handle_peer_response(peer, status, headers, headers_num);
        bytes_left -= parsed;
        buf_start += parsed;
      }

      buf_shift(buf, buf->inuse - bytes_left);

      if (!is_full)
        break;
      // if buffer was full after previous read() then it might be more date in socket, let's read it
    };
  }
}

static bool address_equal(struct sockaddr_storage *addr1, struct sockaddr_storage *addr2) {
  if (addr1->ss_family != addr2->ss_family)
    return false;

  if (addr1->ss_family == AF_INET)
    return memcmp(&((struct sockaddr_in *)addr1)->sin_addr, &((struct sockaddr_in *)addr2)->sin_addr,
                  sizeof(struct in_addr)) == 0;
  else if (addr1->ss_family == AF_INET6)
    return memcmp(&((struct sockaddr_in6 *)addr1)->sin6_addr, &((struct sockaddr_in6 *)addr1)->sin6_addr,
                  sizeof(struct in6_addr)) == 0;
  else
    return false;
}

static void address_cpy(struct sockaddr_storage *dest, struct sockaddr *src) {
  size_t size = src->sa_family == AF_INET ? sizeof(struct sockaddr_in) : sizeof(struct sockaddr_in6);
  memcpy(dest, src, size);
}

static void peer_connect(struct peer *peer) {
  struct addrinfo hints, *result;
  memset(&hints, 0, sizeof(struct addrinfo));
  hints.ai_family = AF_UNSPEC;
  hints.ai_socktype = SOCK_STREAM;

  char port[100];
  snprintf(port, 100, "%d", peer->port);
  int res = getaddrinfo(peer->host, port, &hints, &result);
  if (res != 0 || !result) {
    peer_mark_inactive(peer);
    log_err("getaddrinfo for host %s: %s", peer->host, gai_strerror(res));
    return;
  }

  struct addrinfo *rp;
  for (rp = result; rp != NULL; rp = rp->ai_next) {
    int peerfd = socket(rp->ai_family, rp->ai_socktype, rp->ai_protocol);
    if (peerfd < 0) {
      perror("client socket");
      continue;
    }

    int flags = fcntl(peerfd, F_GETFL, 0);
    fcntl(peerfd, F_SETFL, flags | O_NONBLOCK);

    // we already know address here (even if later connect fails)
    address_cpy(&peer->address, rp->ai_addr);

    int ret = connect(peerfd, (struct sockaddr *)rp->ai_addr, rp->ai_addrlen);
    if (ret == 0) {
      peer->state = ACTIVE;
    } else if (ret < 0 && errno == EINPROGRESS) {
      peer->state = CONNECTING;
    } else {
      log_err("cannot connect to %s:%d - %s", peer->host, peer->port, strerror(errno));
      close(peerfd);
      continue;
    }

    struct epoll_event ev;
    ev.events = EPOLLOUT | EPOLLIN | EPOLLRDHUP;
    ev.data.ptr = &peer->on_event;
    if (epoll_ctl(epollfd, EPOLL_CTL_ADD, peerfd, &ev) < 0) {
      perror("epoll_ctl");
      exit(EXIT_FAILURE);
    }

    peer->fd = peerfd; // caching the peer connection
    break;
  }

  // No hosts found?
  if (peer->state == NEW)
    peer_mark_inactive(peer);

  freeaddrinfo(result);
}

static void send_check_request_to_peer(struct incoming_req *incoming_req, struct peer *peer) {
  if (peer->state == NEW)
    peer_connect(peer);
  if (peer->state == FAILED)
    return;

  int peerfd = peer->fd;
  struct file_check *file_check = incoming_req->file_check;
  const char *prefix = file_check->db ? peer->db_prefix : peer->pkg_prefix;

  struct buffer buff = BUF_INIT();
  const char *filename = file_check->filename;
  if (!is_upstream(peer)) {
    // local repos have flat structure
    filename = flatname(filename);
  }
  debug("[%d] send check request to peer [%d] %s:%d/%s/%s", incoming_req->client->fd, peer->fd, peer->host, peer->port,
        prefix, filename);
  buf_printf(&buff, "HEAD /%s/%s HTTP/1.1\r\nHost: %s:%d\r\n\r\n", prefix, filename, peer->host, peer->port);

  if (peer->state == ACTIVE)
    buf_write(peerfd, &buff);
  else
    buf_append(peer->buffer, &buff);

  struct peer_req *req = malloc(sizeof(struct peer_req));
  memset(req, 0, sizeof(struct peer_req));
  req->incoming_req = incoming_req;
  list_add(&file_check->reqs_head, &req->file_check_req);
  req->peer = peer;
  list_add_tail(&peer->reqs_head, &req->peer_req);

  return;
}

static void handle_repo_request(struct client *client, const char *uri, size_t uri_len, struct phr_header *headers,
                                size_t headers_num) {
  const char *path;
  size_t path_len;
  struct uri_keyvalue params[0]; // upstream url should not have any params
  size_t num_params = 0;
  const char *fragment;
  size_t fragment_len;

  int res = parse_uri_path(uri, uri_len, &path, &path_len, params, &num_params, &fragment, &fragment_len);
  if (res < 0) {
    log_err("[%d] cannot parse repository url '%s'", client->fd, uri);
    client_send_reply(client, 400, "Repository url invalid");
    return;
  }

  // path contains "/repo/" prefix - let's skip it
  assert(path_len >= strlen(REPO_PREFIX));
  path += strlen(REPO_PREFIX);
  path_len -= strlen(REPO_PREFIX);

  if (path_len == 0) {
    log_err("[%d] empty repo url", client->fd);
    client_send_reply(client, 400, "Repository url empty");
    return;
  }

  bool db = false;
  bool skip_check = false;
  if (str_ends_with(path, path_len, ".db") || str_ends_with(path, path_len, ".db.sig")) {
    db = true;
  } else if (str_ends_with(path, path_len, ".files") || str_ends_with(path, path_len, ".files.sig")) {
    skip_check = true;
    // .files are not stored in local repos. Just skip them and send requests straight to upstream
  } else if (str_ends_with(path, path_len, ".pkg.tar.xz")) {
    db = false;
  } else {
    statistics.unknown_repo_requests++;
    client_send_reply(client, 400, "Unknown pacman request");
    return;
  }

  statistics.served_total++;

  struct incoming_req *incoming_req = malloc(sizeof(struct incoming_req));
  memset(incoming_req, 0, sizeof(struct incoming_req));
  incoming_req->client = client;
  list_add_tail(&client->pipeline_head, &incoming_req->pipeline);

  struct file_check *file_check = malloc(sizeof(struct file_check));
  memset(file_check, 0, sizeof(struct file_check));
  incoming_req->file_check = file_check;
  file_check->db = db;
  file_check->filename = strndup(path, path_len);
  INIT_LIST_HEAD(&file_check->reqs_head);

  if (skip_check) {
    debug("[%d] send file request %s straight to upstream", client->fd, file_check->filename);
    incoming_req_redirect_to(incoming_req, &upstream);
    statistics.served_upstream++;
    return;
  }

  struct peer *p;
  socklen_t addr_len = sizeof(struct sockaddr_storage);
  struct sockaddr_storage peer_address;
  getpeername(client->fd, (struct sockaddr *)&peer_address, &addr_len);
  list_for_each(p, &peers_head, list) {
    bool same_host = address_equal(&peer_address, &p->address);

    if (same_host)
      file_check->orig_peer = p;

    if (p->state == FAILED)
      continue;
    if (same_host)
      continue;
    const char *prefix = db ? p->db_prefix : p->pkg_prefix;
    if (!prefix)
      continue;

    send_check_request_to_peer(incoming_req, p);
  }

  if (!list_empty(&file_check->reqs_head)) {
    if (db) {
      // send non-blocking request upstream
      send_check_request_to_peer(incoming_req, &upstream);
      file_check->if_modified_since = header_as_date(headers, headers_num, "If-Modified-Since");
      debug("[%d] if-modified-since %ld", client->fd, file_check->if_modified_since);
    }
    return;
  }

  // otherwise we have no available peers, just send the request upstream
  incoming_req_redirect_to(incoming_req, &upstream);
  statistics.served_upstream++;
  debug("[%d] no suitable local peers", client->fd);
}

static void handle_rpc_request(struct client *client, const char *path, size_t path_len) {
  path += strlen(RPC_PREFIX);
  path_len -= strlen(RPC_PREFIX);
  if (strncmp(path, "ping", path_len) == 0) {
    struct peer *p;
    list_for_each(p, &peers_head, list) {
      if (p->state == FAILED)
        peer_connect(p);
    }
    client_send_ok_reply(client, "text/html", NULL);
  } else {
    client_send_reply(client, 400, "Unknown RPC method");
  }
}

static void handle_incoming_req(struct client *client, const char *path, size_t path_len, struct phr_header *headers,
                                size_t headers_num) {
  debug("[%d] got request %.*s", client->fd, (int)path_len, path);

  if (str_starts_with(path, REPO_PREFIX))
    handle_repo_request(client, path, path_len, headers, headers_num);
  else if (str_starts_with(path, RPC_PREFIX))
    handle_rpc_request(client, path, path_len);
  else
    handle_peer_list(client);
}

static void client_event_handler(uint32_t events, void *data) {
  struct client *client = container_of(data, struct client, on_event);
  int fd = client->fd;

  if (events & EPOLLHUP || events & EPOLLERR || events & EPOLLRDHUP) {
    incoming_client_free(client);
    return;
  }

  if (events & EPOLLIN) {
    size_t bytes_left = 0, bytes_left_last = 0;

    while (true) {
      // TODO: we can make input stack allocated, and only in case of partial parsing allocate on heap
      struct buffer *buf = client->input;
      char *buf_start = &buf->data[0];

      int n = buf_read(fd, buf);
      if (n < 0) {
        perror("buf_read");
        incoming_client_free(client);
        return;
      } else if (n == 0) {
        break;
      }
      bool is_full = buf_full(buf);
      bytes_left_last = bytes_left;
      bytes_left = buf->inuse;

      while (bytes_left > 0) {
        // incoming data might contain multiple HTTP requests (aka HTTP pipeline)
        const char *method, *path;
        int parsed, minor_version;
        struct phr_header headers[HTTP_HEADERS_MAX];
        size_t method_len, path_len, headers_num = HTTP_HEADERS_MAX;
        parsed = phr_parse_request(buf_start, bytes_left, &method, &method_len, &path, &path_len, &minor_version,
                                   headers, &headers_num, bytes_left_last);

        if (parsed == -2) {
          if (is_full) {
            // input buffer is full but still does not contain full request
            // either income buffer is too small or user request is too long
            log_info("[%d] request is too long", fd);
            incoming_client_free(client);
            return;
          }
          // request is incomplete, continue the loop
          break;
        } else if (parsed == -1) {
          log_info("[%d] HTTP request parse error", fd);
          incoming_client_free(client);
          return;
        }
        assert(parsed > 0 && (size_t)parsed <= bytes_left);

        handle_incoming_req(client, path, path_len, headers, headers_num);
        bytes_left -= parsed;
        buf_start += parsed;
      }

      buf_shift(buf, buf->inuse - bytes_left);

      if (!is_full)
        break;
      // if buffer was full after previous read() then it might be more date in socket, let's read it
    };
  }
}

struct http_server {
  int fd;
  handler_t *on_event;
};

static void server_event_handler(uint32_t events, void *data) {
  struct http_server *server = container_of(data, struct http_server, on_event);

  /*
  if (events & EPOLLHUP || events & EPOLLERR || events & EPOLLRDHUP) {
    printf("Closing server\n");
    close(server_data->fd);
    exit(EXIT_FAILURE);
  }
  */

  if (events & EPOLLIN) {
    int clientsd;
    struct sockaddr_storage peeraddr;
    socklen_t salen = sizeof(struct sockaddr_storage);

  restart:
    clientsd = accept(server->fd, (struct sockaddr *)&peeraddr, &salen);
    if (clientsd < 0) {
      if (errno == EINTR)
        goto restart;

      perror("accept");
      return;
    }

    // TODO: set clientfs NONBLOCKING?
    char buffer[INET_ADDRSTRLEN];
    int port;
    const char *res = 0;
    if (peeraddr.ss_family == AF_INET) {
      struct sockaddr_in *s = (struct sockaddr_in *)&peeraddr;
      port = s->sin_port;
      res = inet_ntop(AF_INET, &s->sin_addr, buffer, INET_ADDRSTRLEN);
    } else if (peeraddr.ss_family == AF_INET6) {
      struct sockaddr_in6 *s = (struct sockaddr_in6 *)&peeraddr;
      port = s->sin6_port;
      res = inet_ntop(AF_INET6, &s->sin6_addr, buffer, INET_ADDRSTRLEN);
    } else {
      debug("[%d] unknown peer socket family %d", clientsd, peeraddr.ss_family);
      return;
    }

    if (res) {
      debug("[%d] new client socket from %s:%d", clientsd, buffer, port);
    } else {
      perror("inet_ntop");
      return;
    }

    struct client *client = malloc(sizeof(struct client));
    client->fd = clientsd;
    client->on_event = client_event_handler;
    // TODO: make it dynamic (i.e. only when data should be carried between socket reads)
    client->input = malloc(sizeof(struct buffer));
    buf_init(client->input);
    INIT_LIST_HEAD(&client->pipeline_head);

    struct epoll_event ev;
    ev.events = EPOLLIN | EPOLLRDHUP;
    ev.data.ptr = &client->on_event;

    if (epoll_ctl(epollfd, EPOLL_CTL_ADD, clientsd, &ev) < 0) {
      perror("epoll_ctl");
      return;
    }
  }
}

static void parse_host_str(const char *url, struct peer *peer) {
  // url is 'host:port'
  char *delim = strchr(url, ':');
  if (delim) {
    peer->host = strndup(url, delim - url);
    peer->port = atoi(delim + 1);
  } else {
    peer->host = strdup(url);
    peer->port = 80;
  }
}

static void parse_repo_url(const char *uri, struct peer *upstream) {
  const char *scheme;
  size_t scheme_len;
  const char *host;
  size_t host_len;
  int port;
  const char *path;
  size_t path_len;
  struct uri_keyvalue params[0]; // upstream url should not have any params
  size_t num_params = 0;
  const char *fragment;
  size_t fragment_len;

  int res = parse_uri(uri, strlen(uri), &scheme, &scheme_len, &host, &host_len, &port, &path, &path_len, params,
                      &num_params, &fragment, &fragment_len);
  if (res < 0) {
    log_err("cannot parse upstream repository url '%s'", uri);
    exit(EXIT_FAILURE);
  }

  if (port == -1)
    port = strcmp(scheme, "https") == 0 ? 443 : 80;

  char *prefix = strndup(path + 1, path_len - 1);
  upstream->host = strndup(host, host_len);
  upstream->port = port;
  upstream->pkg_prefix = prefix;
  upstream->db_prefix = prefix;
}

static void peer_init(struct peer *peer) {
  memset(peer, 0, sizeof(struct peer));
  peer->on_event = peer_event_handler;
  peer->buffer = malloc(sizeof(struct buffer));
  buf_init(peer->buffer);
  INIT_LIST_HEAD(&peer->reqs_head);
  peer->state = NEW;
}

/* process a line of the INI file, storing valid values into config struct */
static int parse_handler(void *arg, const char *section, const char *name, const char *value) {
  struct config *cfg = (struct config *)arg;

  if (strcmp(section, "hub") == 0) {
    if (strcmp(name, "upstream") == 0) {
      cfg->upstream = strdup(value);
    } else if (strcmp(name, "port") == 0) {
      cfg->port = atoi(value);
    }
  } else if (strcmp(section, "peer") == 0) {
    // host:port = db_path,pkg_path
    struct peer *p = malloc(sizeof(struct peer));
    peer_init(p);
    parse_host_str(name, p);

    char *repo_str = strdup(value);
    char *comma = strchr(repo_str, ',');
    assert(comma); // we need comma that separates db from package
    *comma = '\0';

    char *db_string = repo_str, *pkg_string = comma + 1;
    p->db_prefix = strdup(db_string);
    p->pkg_prefix = strdup(pkg_string);
    list_add_tail(&peers_head, &p->list);
  }

  return 1;
}

static void parse_config(const char *config_file) {
  if (ini_parse(config_file, parse_handler, &config) < 0) {
    log_warn("Cannot parse config file %s", config_file);
  }
  peer_init(&upstream);
  parse_repo_url(config.upstream, &upstream);
}

#ifndef PACOLOCO_CONFIG_FILE
#define PACOLOCO_CONFIG_FILE "/etc/pacoloco.ini"
#endif

int main(void) {
  parse_config(PACOLOCO_CONFIG_FILE);

  int sockfd = socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK, 0);
  if (sockfd < 0) {
    perror("socket");
    exit(EXIT_FAILURE);
  }

  //  fcntl(sockfd, F_SETFL, O_NONBLOCK)
  // setsockopt(timeout)?
  int on = 1;
  if (setsockopt(sockfd, SOL_SOCKET, SO_REUSEADDR, &on, sizeof(on))) {
    perror("setsockopt");
    exit(EXIT_FAILURE);
  }

  struct sockaddr_in addr;
  memset(&addr, 0, sizeof(addr));
  addr.sin_family = AF_INET;
  addr.sin_port = htons(config.port);
  addr.sin_addr.s_addr = htonl(INADDR_ANY);

  if (bind(sockfd, (struct sockaddr *)&addr, sizeof(addr)) != 0) {
    perror("bind");
    exit(EXIT_FAILURE);
  }

  if (listen(sockfd, SOCKET_BACKLOG) != 0) {
    perror("listen");
    exit(EXIT_FAILURE);
  }

  epollfd = epoll_create1(0);
  if (epollfd < 0) {
    perror("epoll_create1");
    exit(EXIT_FAILURE);
  }

  struct http_server server = {.on_event = server_event_handler, .fd = sockfd};

  struct epoll_event ev;
  ev.events = EPOLLIN;
  ev.data.ptr = &server.on_event;

  log_info("[%d] listening port %d", sockfd, config.port);
  if (epoll_ctl(epollfd, EPOLL_CTL_ADD, sockfd, &ev) < 0) {
    perror("epoll_ctl");
    exit(EXIT_FAILURE);
  }

  while (true) {
    struct epoll_event epoll_events[EPOLL_MAX_EVENTS];
    int num = epoll_wait(epollfd, epoll_events, EPOLL_MAX_EVENTS, -1);
    if (num == -1) {
      perror("epoll_wait");
      exit(EXIT_FAILURE);
    }

    for (int i = 0; i < num; i++) {
      uint32_t events = epoll_events[i].events;
      void *data = epoll_events[i].data.ptr;
      // data represents a user data structure
      // the first field in this structure should be event handler
      (*(handler_t **)data)(events, data);
    }
  }

  return 0;
}
