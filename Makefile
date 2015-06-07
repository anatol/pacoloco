CFLAGS += -W -Wall -std=gnu11 -g -DDEBUG
#LDFLAGS +=
#CFLAGS += -O1 -g -fsanitize=address -fno-omit-frame-pointer

all: pacoloco

%.c: %.rl uriparser_common.rl
	ragel $<

pacoloco: pacoloco.c ini.c picohttpparser.c uriparser.c uripathparser.c
	$(CC) -o $@ $^ $(CFLAGS) $(LDFLAGS)

clean:
	rm -f pacoloco uriparser.c uripathparser.c
