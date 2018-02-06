#ifndef __URL_PARSER_H_
#define __URL_PARSER_H_

#include <sys/types.h>

#define URI_PARSE_ERR -1
#define URI_TOOMANYPARAMS_ERR -2

// contains name and value of a parameter (value == NULL if is a param without value
struct uri_keyvalue {
    const char *name;
    size_t name_len;
    const char *value;
    size_t value_len;
};

// if port is absent then the return value is set to -1
int parse_uri(const char *buf_start, size_t buf_len, const char **scheme, size_t *scheme_len, const char **host,
              size_t *host_len, int *port, const char **path, size_t *path_len, struct uri_keyvalue *params,
              size_t *num_params, const char **fragment, size_t *fragment_len);

int parse_uri_path(const char *buf_start, size_t buf_len, const char **path, size_t *path_len,
                   struct uri_keyvalue *params, size_t *num_params, const char **fragment, size_t *fragment_len);

#endif