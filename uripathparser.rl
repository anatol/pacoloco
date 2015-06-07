#include <stddef.h>
#include "uriparser.h"

%%{
  machine uripath_parser;
  include uriparser_common "uriparser_common.rl";
  main := full_path;
  write data;
}%%

int parse_uri_path(const char* buf_start, size_t buf_len,
                   const char** path, size_t* path_len,
                   struct uri_keyvalue* params, size_t* num_params,
                   const char** fragment, size_t* fragment_len)
{
  const char *p = buf_start;
  const char *pe = p + buf_len;
  int cs;
  const char *eof = pe;
  int err = 0;

  *path = NULL;
  *path_len = 0;
  size_t max_params = *num_params;
  *num_params = 0;
  *fragment = NULL;
  *fragment_len = 0;

  %%{
    write init;
    write exec;
  }%%

  if (err) {
    return err;
  } else if (cs < uripath_parser_first_final) {
    return URI_PARSE_ERR;
  }

  return 0;
}
