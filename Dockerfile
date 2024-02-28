FROM golang:1.21

ADD ./bin/labeler /bin/labeler
ENTRYPOINT [ "/bin/labeler" ]