// TODO:
// make ':' working with ipv6

%%{
  machine uriparser_common;

  scary    = ( cntrl | 127 | ' ' | '"' | '#' | '<' | '>' ) ;
  schmchar = ( lower | digit | '+' | '-' | '.' ) ;
  hostchar = any -- ( scary | '/' | '?' | '#' | ':' ) ;
  pathchar = any -- ( scary | '?' | '#' ) ;
  quervalchar = any -- ( scary | '#' | '&') ;
  querkeychar = quervalchar -- '=' ;
  fragchar = any -- ( scary ) ;

  action scheme_start {
    *scheme = p;
  }

  action scheme_end {
    *scheme_len = p - *scheme;
  }

  action host_start {
    *host = p;
  }

  action host_end {
    *host_len = p - *host;
  }

  action querykey_start {
    if (*num_params >= max_params) {
      err = URI_TOOMANYPARAMS_ERR;
      fbreak;
    }
    params[*num_params].name = p;
    params[*num_params].value = NULL;
  }

  action querykey_end {
    params[*num_params].name_len = p - params[*num_params].name;
  }

  action queryvalue_start {
    params[*num_params].value = p;
  }

  action queryvalue_end {
    params[*num_params].value_len = p - params[*num_params].value;
  }

  action queryentry_end {
    (*num_params)++;
  }

  action frag_start {
    *fragment = p;
  }

  action frag_end {
    *fragment_len = p - *fragment;
  }

  action path_start {
    *path = p;
  }

  action path_end {
    *path_len = p - *path;
  }

  action port_start {
    *port = 0;
  }

  action port {
    *port = *port * 10 + fc - '0';
  }

  scheme = schmchar* >scheme_start %scheme_end;
  host = hostchar+ >host_start %host_end;
  port = ':' digit+ >port_start $port;
  queryentry = querkeychar+ >querykey_start %querykey_end ('=' quervalchar* >queryvalue_start %queryvalue_end)? %queryentry_end;
  query = '?' queryentry? ('&' queryentry)*;
  fragment = '#' (fragchar* >frag_start %frag_end);
  path = ('/' pathchar*) >path_start %path_end;
  full_path = path query? fragment?;
  uri = scheme '://' host port? path? query? fragment?;
}%%
